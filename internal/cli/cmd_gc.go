package cli

import (
	"context"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/gc"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// newGCCommand is the `wi gc` factory. gc is the one WORKSPACE-WIDE verb — it sweeps every
// isolate task, not a single named one — so unlike every other mutating command it takes NO
// operand: an extra arg is a clean usage refusal rather than a silently-ignored task name.
// The --dry-run plan/act split rides the global flag (DryRunFrom), not a positional.
func newGCCommand(l layout.Layout, g *git.Git, args []string) (Command, error) {
	if len(args) != 0 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi gc [--dry-run]"}
	}
	return &gcCmd{layout: l, git: g}, nil
}

// gcCmd is the seam between the evidence-positive reclamation sweep (gc.Inspect read-only +
// gc.Collect action, DESIGN §7.1/§7.5 HEAL-2) and the envelope contract. It has a plan/act
// split keyed on the context's --dry-run flag:
//   - --dry-run → gc.Inspect (read-only, lock-free): every ClassReclaimable cell becomes a
//     planned[] entry and every blocked_work/orphan cell a blocked[] would-block, with NO
//     top-level error so the plan stays exit 0 (SHAPE-DRYRUN-EXIT0). Action is read;
//   - otherwise → gc.Collect (per-task under the isolate-state lock): the per-cell outcomes
//     project onto repos[], and a sweep that did not fully complete returns a (result,
//     *CommandError) refusal carrying the blocking cells in repos[] (envelopeFor threads
//     Repos, NOT Blocked, onto a failure envelope — the same shape isolate rm/repair use).
//
// Two decisions, both recorded in PROGRESS.md, distinguish gc from the single-task verbs:
//
//   - #GC-ID: gc is workspace-wide, so a cell's identity is (task, repo) — but the frozen
//     repos[]/planned[]/blocked[] entries key on a single repo string (no task field, and
//     adding one would move the frozen envelope shape). So each cell is identified by the
//     composite "<task>/<repo>" projected into that field. The wire shape is untouched (the
//     field is free text agents never branch on as an enum), and every cell is unambiguous.
//
//   - #GC-EXIT: the top-level refusal kind reflects the actionable cause. A blocked_work /
//     orphan / per-cell fault is a conflict (exit 4) — deliberate intervention is required.
//     A sweep blocked ONLY because a task is busy (its lock is held by an in-flight op) is
//     lock_held (exit 6) — transient retry-later contention, not a standing refusal. Conflict
//     dominates a mix (an orphan does not become transient because another task is also busy).
//
// The headline Action on the act path is noop (mirror of decision #RP): a sweep has no single
// verb in the closed Action enum — its heterogeneous per-cell effects (reclaim→removed,
// block→noop) are authoritative in repos[].action. It never assembles an envelope or chooses
// an exit code; the pipeline owns that.
type gcCmd struct {
	layout layout.Layout
	git    *git.Git
}

func (c *gcCmd) Run(ctx context.Context) (*Result, error) {
	if DryRunFrom(ctx) {
		return c.dryRun(ctx)
	}
	return c.collect(ctx)
}

// dryRun is the read-only plan path: Inspect the workspace and report what Collect WOULD do,
// without taking any lock or touching disk. A would-block cell rides blocked[] (not a
// top-level error), so the plan is exit-neutral. The live cells gc never touches are omitted
// entirely — they are neither planned nor blocked.
func (c *gcCmd) dryRun(ctx context.Context) (*Result, error) {
	cands, err := gc.Inspect(ctx, c.layout, c.git)
	if err != nil {
		return nil, fmt.Errorf("gc (dry-run inspect): %w", err)
	}
	var planned []contract.PlanItem
	var blocked []contract.BlockItem
	for _, cand := range cands {
		id := cellID(cand.Task, cand.Repo)
		switch gc.Classify(cand) {
		case gc.ClassLive:
			continue // not gc's to touch — HEAL-1's domain
		case gc.ClassReclaimable:
			planned = append(planned, contract.PlanItem{
				Repo:   id,
				Action: "reclaim",
				Detail: "owned, clean, not ahead of base, not live — would remove the worktree and clear its marker",
			})
		case gc.ClassBlockedWork:
			blocked = append(blocked, contract.BlockItem{
				Repo:   id,
				Kind:   contract.KindConflict,
				Reason: blockedWorkReason(),
			})
		case gc.ClassOrphanUnexplained:
			blocked = append(blocked, contract.BlockItem{
				Repo:   id,
				Kind:   contract.KindConflict,
				Reason: orphanReason(cand.Task, cand.Repo),
			})
		}
	}
	return &Result{Action: contract.ActionRead, Planned: planned, Blocked: blocked}, nil
}

// collect is the mutating sweep path: drive gc.Collect under each task's lock and project its
// CollectResult onto repos[] + the exit code. A clean sweep (nothing blocked) is a noop
// success; a sweep with any conflict-class block is a conflict refusal; a sweep blocked only
// by a busy task is a lock_held refusal (decision #GC-EXIT).
func (c *gcCmd) collect(ctx context.Context) (*Result, error) {
	res, err := gc.Collect(ctx, c.layout, c.git, OpIDFrom(ctx))
	if err != nil {
		return nil, fmt.Errorf("gc: %w", err)
	}

	repos := make([]contract.RepoResult, 0, len(res.Repos))
	conflictBlock, busyBlock := false, false
	for _, oc := range res.Repos {
		rr, isConflict, isBusy := projectCollectOutcome(oc)
		repos = append(repos, rr)
		conflictBlock = conflictBlock || isConflict
		busyBlock = busyBlock || isBusy
	}

	switch {
	case conflictBlock:
		return &Result{Action: contract.ActionNoop, Repos: repos},
			&CommandError{
				Kind:    contract.KindConflict,
				Action:  contract.ActionNoop,
				Message: "workspace not fully reclaimed — see repos[] for the blocking cell(s)",
				Help:    "land or discard blocked work; wi will not auto-prune an unexplained orphan (DESIGN §7.1)",
			}
	case busyBlock:
		return &Result{Action: contract.ActionNoop, Repos: repos},
			&CommandError{
				Kind:    contract.KindLockHeld,
				Action:  contract.ActionNoop,
				Message: "workspace not fully reclaimed — a task is busy (its lock is held); see repos[]",
				Help:    "another wi operation holds that task's lock; retry gc when it finishes",
			}
	default:
		return &Result{Action: contract.ActionNoop, Repos: repos}, nil
	}
}

// projectCollectOutcome maps one gc.CollectOutcome onto the wire RepoResult and reports
// whether it is a conflict-class block (blocked_work / orphan / per-cell fault) or a busy-task
// skip — the two buckets the act path's top-level kind is chosen from (decision #GC-EXIT).
//
// A reclaimed cell is action=removed. A blocked_work / orphan cell is action=noop carrying a
// per-cell conflict error sub-coded blocked_work / orphan_unexplained (the loud §7.1 surface).
// A per-cell git/IO fault is internal. A busy-task skip is the one outcome gc.Collect emits
// with NO repo (it is per-task, not per-cell): it is identified by the task alone and surfaces
// as a lock_held per-row note.
func projectCollectOutcome(oc gc.CollectOutcome) (rr contract.RepoResult, isConflict, isBusy bool) {
	// The busy-task skip is the only outcome with an empty Repo (gc.Collect emits it per task,
	// before any cell is examined). Surface it keyed on the task, as lock_held.
	if oc.Repo == "" {
		return contract.RepoResult{
			Repo:   oc.Task,
			Action: contract.ActionNoop,
			Error:  &contract.Error{Kind: contract.KindLockHeld, Message: oc.Reason, Repo: oc.Task},
		}, false, true
	}

	id := cellID(oc.Task, oc.Repo)
	if oc.Reclaimed {
		return contract.RepoResult{Repo: id, Action: contract.ActionRemoved}, false, false
	}

	rr = contract.RepoResult{Repo: id, Action: contract.ActionNoop}
	switch {
	case oc.Err != nil:
		rr.Error = &contract.Error{Kind: contract.KindInternal, Message: oc.Err.Error(), Repo: id}
	case oc.Class == gc.ClassBlockedWork:
		rr.Error = &contract.Error{Kind: contract.KindConflict, Code: "blocked_work", Message: oc.Reason, Repo: id}
	case oc.Class == gc.ClassOrphanUnexplained:
		rr.Error = &contract.Error{Kind: contract.KindConflict, Code: "orphan_unexplained", Message: oc.Reason, Repo: id}
	}
	return rr, true, false
}

// cellID is the composite "<task>/<repo>" identity gc projects into the frozen single-string
// repo field of a wire row (decision #GC-ID), since gc is the one workspace-wide verb whose
// rows span multiple tasks.
func cellID(task, repo string) string { return task + "/" + repo }

// blockedWorkReason is the blocked_work sub-reason — a worktree carrying uncommitted or
// unmerged work that gc must never destroy. It matches the message gc.Collect emits on the
// mutating path so the dry-run plan and the act-path refusal read identically.
func blockedWorkReason() string {
	return "blocked_work: worktree carries uncommitted or unmerged work — land or discard it deliberately"
}
