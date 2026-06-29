// Package isolate is the domain core of `wi isolate new`: it materializes one
// detached worktree per declared repo off the SSOT base, recording progress in
// internal/state as it goes (DESIGN §1, §6.3). It is the partial-success-critical
// command — the one place wi's "durable partial success" contract is enforced.
//
// Orchestration (DESIGN §6.3). Under the isolate-state:<task> lock (DESIGN §6.1),
// New first writes an IsolateRecord with every requested repo at StagePending —
// the durable statement of intent that makes the op resumable — and ONLY THEN
// materializes repos one at a time, in request order. Each repo is, in this exact
// order:
//
//	1. git worktree add --detach   (the linked worktree off refs/heads/<base>)
//	2. CreateOwnedRef              (refs/wi/owned/<task>/<repo> — evidence-positive
//	                                ownership BEFORE we claim the repo "created", so a
//	                                crash after step 1 leaves a wi-owned reclaimable
//	                                worktree, never an unexplained orphan — DESIGN §7.1)
//	3. state.UpdateRepoStage(...Created)   (flip the durable registry to created)
//
// It is STOP-ON-FIRST-FAIL with durable, NOT-rolled-back completed repos: the
// first repo that fails halts the run, every repo completed before it stays on
// disk and in the registry, and repos after it are never attempted (they remain
// StagePending). The result carries StatusPartial so the CLI emits exit 2; the run
// is resumable because the registry reflects EXACTLY the completed set. A per-repo
// failure is NOT a Go error from New — it is recorded in the result; New's error
// return is reserved for failures that prevent the op from running at all (a held
// lock → *lock.HeldError → exit 6, an unwritable initial record, an unsafe name).
//
// SSOT invariants (DESIGN §5): worktrees are added off refs/heads/<base> but the
// base ref is NEVER moved here (only git.FastForwardBaseRef advances a base ref),
// and the SSOT working tree is never dirtied — a linked worktree shares the object
// store without touching the SSOT checkout.
package isolate

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// RepoSpec is one repo to materialize: its wi-internal name (→ repos/<name> SSOT,
// isolas/<task>/<name> worktree) and the EFFECTIVE base branch the worktree
// detaches at. The CLI resolves these from the manifest (config.Repo) before
// calling New, so this package stays decoupled from manifest parsing.
type RepoSpec struct {
	Name string
	Base string
}

// Status is the overall outcome of a New run.
type Status string

const (
	// StatusComplete: every requested repo was materialized.
	StatusComplete Status = "complete"
	// StatusPartial: the run stopped on the first repo that failed; repos before
	// it are durably complete, repos after it were not attempted (exit 2).
	StatusPartial Status = "partial"
)

// RepoOutcome is one repo's result within a New run. Stage is StageCreated once the
// worktree+marker+registry flip all succeeded, else StagePending (not reached). Err
// is set on exactly the repo that failed (the one that triggered stop-on-first-fail).
type RepoOutcome struct {
	Repo  string
	Base  string
	Stage state.Stage
	Path  string // worktree path (set once the path was computed)
	SHA   string // worktree HEAD sha (= base tip at add) on success
	Err   error
}

// Result is the outcome of a New run: the task/op identity, the overall Status, and
// a per-repo outcome in request order. The CLI projects this onto the envelope's
// repos[] and the exit code (complete → 0, partial → 2).
type Result struct {
	Task   string
	OpID   string
	Status Status
	Repos  []RepoOutcome
}

// New materializes an isolate for task: one detached worktree per repo in specs,
// off each repo's SSOT base, under the isolate-state:<task> lock. See the package
// doc for the durable-partial-success contract. l must be Bootstrap'd (the lock and
// state dirs must exist). opID identifies the creating op in the registry.
func New(ctx context.Context, l layout.Layout, g *git.Git, task, opID string, specs []RepoSpec) (Result, error) {
	key, err := lock.IsolateState(task)
	if err != nil {
		return Result{}, err
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		return Result{}, err // *lock.HeldError → exit 6 (DESIGN §6.1)
	}
	defer func() { _ = held.Release() }()

	// The isolas/<task>/ parent must exist before any worktree leaf is added.
	taskDir, err := l.TaskDir(task)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("isolate: create task dir %s: %w", taskDir, err)
	}

	// Durable statement of intent: every requested repo recorded StagePending
	// BEFORE any materialization, so a crash leaves a resumable registry (§6.3).
	stateDir := l.StateDir()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	if err := state.Store(stateDir, state.NewIsolateRecord(task, opID, names)); err != nil {
		return Result{}, fmt.Errorf("isolate: write initial record for %q: %w", task, err)
	}

	res := Result{Task: task, OpID: opID, Status: StatusComplete, Repos: make([]RepoOutcome, 0, len(specs))}
	for i := range specs {
		s := specs[i]
		oc := RepoOutcome{Repo: s.Name, Base: s.Base, Stage: state.StagePending}

		ssot, err := l.Repo(s.Name)
		if err == nil {
			oc.Path, err = l.Isolate(task, s.Name)
		}
		if err == nil {
			oc.SHA, err = materializeRepo(ctx, g, ssot, oc.Path, task, s.Name, s.Base, stateDir)
		}
		if err != nil {
			// Stop-on-first-fail: record this repo's failure, mark every later repo
			// not-attempted (StagePending), and return a durable partial success.
			oc.Err = err
			res.Status = StatusPartial
			res.Repos = append(res.Repos, oc)
			for _, rest := range specs[i+1:] {
				res.Repos = append(res.Repos, RepoOutcome{Repo: rest.Name, Base: rest.Base, Stage: state.StagePending})
			}
			return res, nil
		}

		oc.Stage = state.StageCreated
		res.Repos = append(res.Repos, oc)
	}
	return res, nil
}

// RemoveStatus is the overall outcome of a Remove run.
type RemoveStatus string

const (
	// RemoveComplete: every targeted repo passed the evidence-positive gates and was
	// reclaimed.
	RemoveComplete RemoveStatus = "complete"
	// RemoveBlocked: at least one targeted repo was a HARD BLOCK (an unexplained
	// orphan, DESIGN §7.1) or hit a per-repo error, so it was NOT reclaimed.
	RemoveBlocked RemoveStatus = "blocked"
)

// ErrRepoNotInIsolate reports that a repo named on a Remove targeted the isolate but
// is absent from its registry record — wi has no recorded materialization for it, so
// there is nothing it can prove it owns to reclaim.
var ErrRepoNotInIsolate = errors.New("isolate: repo not in isolate record")

// RemoveOutcome is one targeted repo's result within a Remove run. Exactly one of
// three states holds: Removed (reclaimed — worktree + marker gone, dropped from the
// registry); a HARD BLOCK (Removed=false, Reason set, Err=nil — an unexplained
// orphan surfaced loudly per DESIGN §7.1, never auto-pruned, kept on disk and in the
// registry); or a per-repo error (Removed=false, Err set — a git/IO fault or a repo
// not in the record).
type RemoveOutcome struct {
	Repo    string
	Removed bool
	Reason  string // orphan_unexplained sub-reason when blocked (Err==nil)
	Err     error  // a hard per-repo failure (Reason=="")
}

// RemoveResult is the outcome of a Remove run: the task identity, the overall
// Status, and a per-repo outcome in target order. The CLI projects this onto the
// envelope's repos[]/blocked[] and the exit code.
type RemoveResult struct {
	Task   string
	Status RemoveStatus
	Repos  []RemoveOutcome
}

// Remove reclaims an isolate's materialized worktrees under the isolate-state:<task>
// lock, honoring the evidence-positive reclamation contract (DESIGN §7.1): a repo's
// worktree is reclaimed ONLY if all three gates pass —
//
//	1. wi can PROVE it owns the worktree: the marker ref refs/wi/owned/<task>/<repo>
//	   exists (a missing marker is an unexplained orphan — wi never created it, or
//	   lost the evidence);
//	2. the worktree is CLEAN (no modified/untracked files);
//	3. it is NOT ahead of base — realized in v0 as: the worktree HEAD still equals the
//	   marker's recorded sha (the base tip captured at creation). The per-repo base
//	   branch is not persisted in state.RepoRecord, so the MARKER is the base evidence;
//	   a HEAD that moved past it carries local work and is "ahead of base".
//
// Any repo that fails a gate is a HARD BLOCK reported as an unexplained orphan
// (Reason set) — never auto-pruned, never --force'd (no --force in MVP, DESIGN §7.2),
// left intact on disk and in the registry. Verified repos are reclaimed
// (RemoveWorktree, which itself refuses a dirty tree and passes no --force, then
// DeleteOwnedRef) and dropped from the registry; when the LAST recorded repo is
// reclaimed the record is deleted and the (now-empty) task dir removed best-effort.
//
// If repos is empty, every repo in the record is targeted (full teardown); otherwise
// exactly the named repos are. A held lock returns *lock.HeldError (exit 6); a task
// with no record returns state.ErrNoRecord (the isolate does not exist). A per-repo
// gate failure is NOT a Go error — it is recorded in the result; Remove's error
// return is reserved for failures that prevent the op from running at all.
func Remove(ctx context.Context, l layout.Layout, g *git.Git, task string, repos []string) (RemoveResult, error) {
	key, err := lock.IsolateState(task)
	if err != nil {
		return RemoveResult{}, err
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		return RemoveResult{}, err // *lock.HeldError → exit 6 (DESIGN §6.1)
	}
	defer func() { _ = held.Release() }()

	stateDir := l.StateDir()
	rec, err := state.Load(stateDir, task)
	if err != nil {
		return RemoveResult{}, err // state.ErrNoRecord → not_found; else a read fault
	}

	recorded := make(map[string]bool, len(rec.Repos))
	for _, rr := range rec.Repos {
		recorded[rr.Repo] = true
	}
	// Empty target set means "every recorded repo" (full teardown), in record order.
	targets := repos
	if len(targets) == 0 {
		targets = make([]string, len(rec.Repos))
		for i, rr := range rec.Repos {
			targets[i] = rr.Repo
		}
	}

	res := RemoveResult{Task: task, Status: RemoveComplete, Repos: make([]RemoveOutcome, 0, len(targets))}
	removed := make(map[string]bool, len(targets))
	for _, name := range targets {
		oc := reclaimRepo(ctx, g, l, task, name, recorded[name])
		if oc.Removed {
			removed[name] = true
		} else {
			res.Status = RemoveBlocked
		}
		res.Repos = append(res.Repos, oc)
	}

	// Drop the reclaimed repos from the registry. When none remain, the isolate no
	// longer exists: delete the record and best-effort remove the empty task dir.
	if len(removed) > 0 {
		kept := rec.Repos[:0]
		for _, rr := range rec.Repos {
			if !removed[rr.Repo] {
				kept = append(kept, rr)
			}
		}
		rec.Repos = kept
		if len(rec.Repos) == 0 {
			if err := state.Delete(stateDir, task); err != nil {
				return res, fmt.Errorf("isolate: delete record for %q after full reclaim: %w", task, err)
			}
			if taskDir, derr := l.TaskDir(task); derr == nil {
				_ = os.Remove(taskDir) // best-effort: succeeds only if now empty
			}
		} else if err := state.Store(stateDir, rec); err != nil {
			return res, fmt.Errorf("isolate: update record for %q after reclaim: %w", task, err)
		}
	}
	return res, nil
}

// reclaimRepo evaluates the three evidence-positive gates for one repo and reclaims
// it iff all pass (DESIGN §7.1). It never moves a base ref and never dirties the SSOT.
func reclaimRepo(ctx context.Context, g *git.Git, l layout.Layout, task, repo string, recorded bool) RemoveOutcome {
	oc := RemoveOutcome{Repo: repo}
	if !recorded {
		oc.Err = ErrRepoNotInIsolate
		return oc
	}
	ssot, err := l.Repo(repo)
	if err != nil {
		oc.Err = err
		return oc
	}
	wt, err := l.Isolate(task, repo)
	if err != nil {
		oc.Err = err
		return oc
	}

	// Gate 1 — ownership: wi must be able to prove it created this worktree.
	marker, exists, err := g.OwnedRefSHA(ctx, ssot, task, repo)
	if err != nil {
		oc.Err = err
		return oc
	}
	if !exists {
		oc.Reason = "orphan_unexplained: no wi ownership marker refs/wi/owned/" + task + "/" + repo
		return oc
	}
	// Gate 2 — clean: a dirty worktree carries uncommitted work.
	clean, err := g.IsClean(ctx, wt)
	if err != nil {
		oc.Err = err
		return oc
	}
	if !clean {
		oc.Reason = "orphan_unexplained: worktree has uncommitted changes"
		return oc
	}
	// Gate 3 — not ahead of base: the marker IS the base evidence (v0), so a HEAD
	// past it carries local commits.
	head, err := g.ResolveRef(ctx, wt, "HEAD")
	if err != nil {
		oc.Err = err
		return oc
	}
	if head != marker {
		oc.Reason = "orphan_unexplained: worktree is ahead of base (HEAD moved past the creation marker)"
		return oc
	}

	// All gates pass — reclaim. RemoveWorktree is a second cleanliness net (no
	// --force, no reset --hard); DeleteOwnedRef clears the now-spent marker.
	if err := g.RemoveWorktree(ctx, ssot, wt); err != nil {
		oc.Err = err
		return oc
	}
	if err := g.DeleteOwnedRef(ctx, ssot, task, repo); err != nil {
		oc.Err = err
		return oc
	}
	oc.Removed = true
	return oc
}

// materializeRepo runs the three-step per-repo materialization in the order the
// evidence-positive contract requires (worktree → marker → registry flip), so any
// crash leaves a wi-owned, reclaimable state rather than an unexplained orphan
// (DESIGN §7.1). It returns the worktree HEAD sha (the base tip at add time) that
// the marker records. It never moves the base ref or dirties the SSOT (DESIGN §5).
func materializeRepo(ctx context.Context, g *git.Git, ssotDir, wtPath, task, repo, base, stateDir string) (string, error) {
	baseRef := "refs/heads/" + base
	sha, err := g.ResolveRef(ctx, ssotDir, baseRef)
	if err != nil {
		return "", err
	}
	if err := g.AddWorktree(ctx, ssotDir, wtPath, baseRef); err != nil {
		return "", err
	}
	if err := g.CreateOwnedRef(ctx, ssotDir, task, repo, sha); err != nil {
		return "", err
	}
	if err := state.UpdateRepoStage(stateDir, task, repo, state.StageCreated); err != nil {
		return "", err
	}
	return sha, nil
}
