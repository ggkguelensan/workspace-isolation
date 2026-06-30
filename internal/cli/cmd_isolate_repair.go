package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// newIsolateRepairCommand is the `wi isolate repair <task>` factory: it validates a single
// safe <task> segment and binds it plus the layout/git driver into a runnable Command.
// repair reconciles the WHOLE isolate (drift is per-isolate — every recorded cell is
// re-observed against its marker and worktree), so unlike `isolate rm` there is NO repo
// subset: an extra operand is a clean usage refusal, not a silently-ignored arg.
func newIsolateRepairCommand(l layout.Layout, g *git.Git, args []string) (Command, error) {
	if len(args) != 1 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi isolate repair <task>"}
	}
	task := args[0]
	if err := layout.ValidateSegment("task", task); err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	return &isolateRepairCmd{layout: l, git: g, task: task}, nil
}

// isolateRepairCmd is the seam between the three-way drift reconciler (DESIGN §7.4 HEAL-1)
// and the envelope contract. It has a plan/act split keyed on the context's --dry-run flag
// (DryRunFrom):
//   - --dry-run → isolate.Inspect (read-only, lock-free): every reconcilable cell becomes a
//     planned[] entry (the RepairAction PlanAction would take) and every orphan becomes a
//     blocked[] would-block verdict, with NO top-level error so the plan stays exit 0
//     (SHAPE-DRYRUN-EXIT0 / decision #D — a dry-run that RAN hangs its verdicts in the
//     additive blocks, never the error). Action is read (it only observed);
//   - otherwise → isolate.Repair (under the isolate-state lock): the per-cell outcomes are
//     projected into repos[] and, if any cell HARD-BLOCKED (an orphan) or errored, the run
//     returns a (result, *CommandError{conflict}) refusal (exit 4) carrying the blocking
//     cells in repos[] — envelopeFor threads Repos, NOT Blocked, onto a failure envelope, so
//     a non-zero exit must surface them there (the same shape `isolate rm` uses).
//
// The headline Action on the mutating path is noop (decision #RP): a reconcile has no single
// verb in the closed Action enum — its heterogeneous per-cell effects (re-materialize→
// created, drop→removed, heal/none→noop) are authoritative in repos[].action. It never
// assembles an envelope or chooses an exit code; the pipeline owns that.
type isolateRepairCmd struct {
	layout layout.Layout
	git    *git.Git
	task   string
}

func (c *isolateRepairCmd) Run(ctx context.Context) (*Result, error) {
	if DryRunFrom(ctx) {
		return c.dryRun(ctx)
	}
	return c.repair(ctx)
}

// dryRun is the read-only plan path: Inspect the isolate and report what Repair WOULD do,
// without taking the lock or touching disk. A would-block orphan rides blocked[] (not a
// top-level error), so the plan is exit-neutral.
func (c *isolateRepairCmd) dryRun(ctx context.Context) (*Result, error) {
	cells, err := isolate.Inspect(ctx, c.layout, c.git, c.task)
	if err != nil {
		return nil, c.classifyInspectErr(err)
	}
	var planned []contract.PlanItem
	var blocked []contract.BlockItem
	for _, cell := range cells {
		action := isolate.PlanAction(cell)
		if action == isolate.RepairBlockOrphan {
			blocked = append(blocked, contract.BlockItem{
				Repo:   cell.Repo,
				Kind:   contract.KindConflict,
				Reason: orphanReason(c.task, cell.Repo),
			})
			continue
		}
		planned = append(planned, contract.PlanItem{
			Repo:   cell.Repo,
			Action: string(action),
			Detail: planDetail(cell, action),
		})
	}
	return &Result{Action: contract.ActionRead, Planned: planned, Blocked: blocked}, nil
}

// repair is the mutating reconcile path: drive isolate.Repair under the lock and project its
// RepairResult onto repos[] + the exit code.
func (c *isolateRepairCmd) repair(ctx context.Context) (*Result, error) {
	res, err := isolate.Repair(ctx, c.layout, c.git, c.task, OpIDFrom(ctx))
	if err != nil {
		var he *lock.HeldError
		if errors.As(err, &he) {
			return nil, &CommandError{
				Kind:    contract.KindLockHeld,
				Message: fmt.Sprintf("isolate %q is busy: %v", c.task, err),
				Help:    "another wi operation holds this task's lock; retry when it finishes",
			}
		}
		if errors.Is(err, state.ErrNoRecord) {
			return nil, c.notFound()
		}
		return nil, fmt.Errorf("isolate repair %q: %w", c.task, err)
	}

	repos := make([]contract.RepoResult, len(res.Repos))
	for i, oc := range res.Repos {
		repos[i] = projectRepairOutcome(oc)
	}

	if res.Status == isolate.RepairBlocked {
		// At least one cell HARD-BLOCKED (orphan) or errored. The reconcilable cells were
		// still reconciled (best-effort-all); this is a refusal, not a clean success — the
		// blocking cells ride in repos[] (envelopeFor does not thread Blocked on failure).
		return &Result{Action: contract.ActionNoop, Repos: repos},
			&CommandError{
				Kind:    contract.KindConflict,
				Action:  contract.ActionNoop,
				Message: fmt.Sprintf("isolate %q not fully reconciled — see repos[] for the blocking cell(s)", c.task),
				Help:    "inspect the worktree(s); wi will not auto-prune an unexplained orphan (DESIGN §7.1)",
			}
	}
	return &Result{Action: contract.ActionNoop, Repos: repos}, nil
}

// classifyInspectErr maps an Inspect error onto the return convention: a missing record is
// not_found (the isolate does not exist), anything else is an unclassified internal fault.
func (c *isolateRepairCmd) classifyInspectErr(err error) error {
	if errors.Is(err, state.ErrNoRecord) {
		return c.notFound()
	}
	return fmt.Errorf("isolate repair %q (dry-run inspect): %w", c.task, err)
}

func (c *isolateRepairCmd) notFound() *CommandError {
	return &CommandError{
		Kind:    contract.KindNotFound,
		Message: fmt.Sprintf("no isolate %q to repair", c.task),
		Help:    "create one with: wi isolate new <task> <repo>…",
	}
}

// projectRepairOutcome maps one isolate.RepairOutcome onto the wire RepoResult. A reconciled
// cell's per-repo action mirrors what was physically done — re-materialize→created,
// drop_record→removed, none/heal_stage→noop (no disk change) — and its healed stage rides
// the stage field. A HARD BLOCK (Reason set, Err nil) is a per-repo conflict sub-coded
// orphan_unexplained (the DESIGN §7.1 loud surface); a per-cell git/IO fault (Err set) is
// internal. A blocking cell is action=noop: nothing was done to it.
func projectRepairOutcome(oc isolate.RepairOutcome) contract.RepoResult {
	rr := contract.RepoResult{Repo: oc.Repo, Action: contract.ActionNoop}
	switch {
	case oc.Reason != "":
		rr.Error = &contract.Error{
			Kind:    contract.KindConflict,
			Code:    "orphan_unexplained",
			Message: oc.Reason,
			Repo:    oc.Repo,
		}
	case oc.Err != nil:
		rr.Error = &contract.Error{
			Kind:    contract.KindInternal,
			Message: oc.Err.Error(),
			Repo:    oc.Repo,
		}
	case oc.Action == isolate.RepairRematerialize:
		rr.Action = contract.ActionCreated
		rr.Stage = string(state.StageCreated)
	case oc.Action == isolate.RepairDropRecord:
		rr.Action = contract.ActionRemoved
	case oc.Action == isolate.RepairHealStage:
		rr.Stage = string(state.StageCreated) // record healed; no disk change → action stays noop
	}
	return rr
}

// planDetail is the human-readable, never-branched-on note describing what the dry-run plan
// would do for a cell (its observed Classification and the resulting action). Agents branch
// on PlanItem.Action; this is for an operator reading the plan.
func planDetail(cell isolate.Cell, action isolate.RepairAction) string {
	switch action {
	case isolate.RepairRematerialize:
		return "worktree missing; would re-add at owned marker " + cell.MarkerSHA
	case isolate.RepairDropRecord:
		return "reclaimed (no marker, no worktree); would drop the stale record"
	case isolate.RepairHealStage:
		return "consistent but stage lags at pending; would heal stage to created"
	default: // RepairNone
		return "consistent; no action"
	}
}

// orphanReason is the orphan_unexplained sub-reason surfaced for an OrphanWorktree cell,
// matching the loud §7.1 message isolate.Repair produces on the mutating path.
func orphanReason(task, repo string) string {
	return "orphan_unexplained: worktree present but no wi ownership marker refs/wi/owned/" + task + "/" + repo
}
