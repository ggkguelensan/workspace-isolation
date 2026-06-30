package gc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// CollectStatus is the overall outcome of a Collect sweep.
type CollectStatus string

const (
	// CollectComplete: every on-disk worktree was either reclaimed (ClassReclaimable) or
	// was nothing for gc to act on (a live cell, left untouched). No hard block, no error.
	CollectComplete CollectStatus = "complete"
	// CollectBlocked: at least one cell was a HARD BLOCK (blocked_work or
	// orphan_unexplained), a busy task was skipped, or a per-cell reclaim faulted. The
	// reclaimable cells were still reclaimed (best-effort-all): a block never aborts the rest.
	CollectBlocked CollectStatus = "blocked"
)

// CollectOutcome is one cell's result within a Collect sweep, carrying its own (Task,
// Repo) identity since the sweep spans the whole workspace. Exactly one terminal shape
// holds: Reclaimed (the worktree + spent marker were removed); a HARD BLOCK (Reclaimed
// false, Reason set, Err nil — a blocked_work/orphan_unexplained cell or a busy task,
// left fully intact and surfaced loudly per §7.1); or a per-cell error (Reclaimed false,
// Err set — a git/IO fault while removing). Live cells are NOT emitted: they are not gc's
// to touch, so they never appear as outcomes.
type CollectOutcome struct {
	Task      string
	Repo      string
	Class     Class
	Reclaimed bool
	Reason    string // hard-block sub-reason (Err==nil); empty when Reclaimed or Err set
	Err       error  // a hard per-cell failure (Reason=="")
}

// CollectResult is the outcome of a workspace gc sweep: the overall Status plus a
// per-cell outcome for every cell gc acted on or blocked on (live cells omitted). The
// CLI projects this onto the envelope's repos[] (reclaimed) / blocked[] and the exit code.
type CollectResult struct {
	Status CollectStatus
	Repos  []CollectOutcome
}

// Collect is the ACTION half of `wi gc` (HEAL-2, DESIGN §7.1): the workspace-wide sweep
// that reclaims every provably-wi-owned, clean, not-ahead, not-live leftover worktree and
// leaves everything else strictly alone. It enumerates the isolate tasks on disk and
// reconciles each under its own isolate-state:<task> lock, RE-observing the cell's signals
// under the lock (never trusting Inspect's lock-free snapshot across the acquire) and
// reclaiming ONLY ClassReclaimable cells: RemoveWorktree (no --force, a second cleanliness
// net) then DeleteOwnedRef (clear the now-spent marker). Every other class is preserved —
// blocked_work (would destroy uncommitted/unmerged work), orphan_unexplained (no provenance
// to prove wi's ownership), and live (HEAL-1's domain, not gc's). It moves no base ref,
// uses no --prune, and dials no network.
//
// It is best-effort-all and per-task independent: a hard block, a per-cell fault, or a busy
// (locked) task is recorded and the sweep continues, so one in-flight op or one orphan never
// blocks reclaiming unrelated leftovers. A genuine environment fault (an unreadable isolas
// dir, a non-HeldError lock failure, a torn registry record) is returned as an error with the
// partial result, since it is not expressible as a per-cell verdict.
func Collect(ctx context.Context, l layout.Layout, g *git.Git, opID string) (CollectResult, error) {
	isolasDir := l.IsolasDir()
	taskEntries, err := os.ReadDir(isolasDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return CollectResult{Status: CollectComplete}, nil // no isolate ever created
		}
		return CollectResult{}, fmt.Errorf("gc: list isolate tasks in %s: %w", isolasDir, err)
	}

	res := CollectResult{Status: CollectComplete}
	for _, te := range taskEntries {
		if !te.IsDir() {
			continue
		}
		outcomes, blocked, err := collectTask(ctx, l, g, isolasDir, te.Name(), opID)
		if err != nil {
			return res, err
		}
		res.Repos = append(res.Repos, outcomes...)
		if blocked {
			res.Status = CollectBlocked
		}
	}
	return res, nil
}

// collectTask reconciles one task's on-disk worktrees under its isolate-state lock. A
// HELD lock means an isolate op is in flight on this task — the workspace-gc counterpart
// of "journaled as live": the task is SKIPPED (recorded as a busy block), never fought.
// This deliberately diverges from the single-task Remove/Repair verbs (which surface a
// held lock as their own exit-6 contention): a workspace sweep must not let one in-flight
// op abort reclamation of unrelated leftovers. Returns the per-cell outcomes and whether
// the task contributed a block.
func collectTask(ctx context.Context, l layout.Layout, g *git.Git, isolasDir, task, opID string) ([]CollectOutcome, bool, error) {
	key, err := lock.IsolateState(task)
	if err != nil {
		return nil, false, err
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		var he *lock.HeldError
		if errors.As(err, &he) {
			// Busy: an isolate op holds the lock. Skip — gc never fights a live op.
			return []CollectOutcome{{Task: task, Reason: "busy: isolate-state lock held — task skipped"}}, true, nil
		}
		return nil, false, err // a real lock fault (not contention)
	}
	defer func() { _ = held.Release() }()
	_ = held.Stamp(opID) // best-effort holder metadata (mirrors New/Remove/Repair)

	// This task's live cells: only this task's record bears on its cells' liveness. A
	// missing record (the common leftover case) means no cell is live; a torn record is a
	// hard environment fault, surfaced (never silently treated as "not live").
	liveRepos := map[string]bool{}
	if rec, err := state.Load(l.StateDir(), task); err == nil {
		for _, rr := range rec.Repos {
			liveRepos[rr.Repo] = true
		}
	} else if !errors.Is(err, state.ErrNoRecord) {
		return nil, false, fmt.Errorf("gc: load record for %q: %w", task, err)
	}

	taskDir := filepath.Join(isolasDir, task)
	repoEntries, err := os.ReadDir(taskDir)
	if err != nil {
		return nil, false, fmt.Errorf("gc: list isolate worktrees in %s: %w", taskDir, err)
	}

	var outcomes []CollectOutcome
	blocked := false
	for _, re := range repoEntries {
		if !re.IsDir() {
			continue
		}
		repo := re.Name()
		cand, err := observeCandidate(ctx, l, g, task, repo, liveRepos[repo])
		if err != nil {
			return nil, false, err
		}
		switch Classify(cand) {
		case ClassLive:
			continue // not gc's to touch — HEAL-1's domain, emitted as no outcome
		case ClassReclaimable:
			oc := CollectOutcome{Task: task, Repo: repo, Class: ClassReclaimable}
			if err := reclaim(ctx, g, l, task, repo); err != nil {
				oc.Err = err
				blocked = true
			} else {
				oc.Reclaimed = true
			}
			outcomes = append(outcomes, oc)
		case ClassBlockedWork:
			outcomes = append(outcomes, CollectOutcome{Task: task, Repo: repo, Class: ClassBlockedWork,
				Reason: "blocked_work: worktree carries uncommitted or unmerged work — land or discard it deliberately"})
			blocked = true
		case ClassOrphanUnexplained:
			outcomes = append(outcomes, CollectOutcome{Task: task, Repo: repo, Class: ClassOrphanUnexplained,
				Reason: "orphan_unexplained: worktree present but no wi ownership marker refs/wi/owned/" + task + "/" + repo})
			blocked = true
		}
	}

	// Best-effort remove the now-empty task dir (succeeds only if every cell was
	// reclaimed and nothing blocked remains) — mirrors Remove/Repair's empty-dir cleanup.
	_ = os.Remove(taskDir)
	return outcomes, blocked, nil
}

// reclaim performs the terminal reclamation for a cell Classify has already judged
// ClassReclaimable: RemoveWorktree (no --force — a second cleanliness net that refuses a
// dirty tree) then DeleteOwnedRef (clear the now-spent ownership marker). It moves no base
// ref and dials no network. This is the gc twin of isolate.reclaimRepo's terminal action,
// minus the gates — gc.Classify is the gate.
func reclaim(ctx context.Context, g *git.Git, l layout.Layout, task, repo string) error {
	ssot, err := l.Repo(repo)
	if err != nil {
		return err
	}
	wt, err := l.Isolate(task, repo)
	if err != nil {
		return err
	}
	if err := g.RemoveWorktree(ctx, ssot, wt); err != nil {
		return err
	}
	if err := g.DeleteOwnedRef(ctx, ssot, task, repo); err != nil {
		return err
	}
	return nil
}
