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

// newIsolateRmCommand is the `wi isolate rm <task> [<repo>…]` factory: it validates the
// <task> segment (a traversing name is a clean usage refusal HERE) and binds the optional
// <repo>… subset. Unlike `isolate new`, zero repos is VALID — it means full teardown (every
// recorded repo). A named repo that is not a member is NOT a usage error: it is well-formed
// and surfaces as a per-repo not_found from the domain (ErrRepoNotInIsolate), so it is not
// segment-checked here.
func newIsolateRmCommand(l layout.Layout, g *git.Git, args []string) (Command, error) {
	if len(args) < 1 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi isolate rm <task> [<repo>…]"}
	}
	task := args[0]
	if err := layout.ValidateSegment("task", task); err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	return &isolateRmCmd{layout: l, git: g, task: task, repos: args[1:]}, nil
}

// isolateRmCmd is the teardown seam between the evidence-positive reclamation core
// (isolate.Remove, DESIGN §7.1) and the envelope contract. Run drives Remove and maps its
// outcome onto the return convention (decision #RD):
//   - RemoveComplete → a removed Result (exit 0);
//   - mixed (≥1 reclaimed AND ≥1 not) → the DURABLE PARTIAL (result, *CommandError{partial,
//     Action: removed}) carrying per-repo detail (decision #D) — re-running reclaims any
//     now-unblocked repos, so it is resumable, not a clean refusal;
//   - nothing reclaimed with ≥1 orphan hard-block → a full refusal *CommandError{conflict}
//     (exit 4): the on-disk state conflicts with what wi can prove it owns;
//   - nothing reclaimed and every non-removed repo is merely not a member → not_found;
//   - *lock.HeldError → lock_held; state.ErrNoRecord → not_found (+`wi isolate new`).
//
// Blocked repos ride in repos[] (per-repo Error, code orphan_unexplained), NOT Blocked[]:
// envelopeFor threads only Repos/Warnings/Next onto a failure envelope, so a non-zero exit
// must surface them in repos[] (decision #RD). It never assembles an envelope or chooses an
// exit code — the pipeline owns that.
type isolateRmCmd struct {
	layout layout.Layout
	git    *git.Git
	task   string
	repos  []string
}

func (c *isolateRmCmd) Run(ctx context.Context) (*Result, error) {
	res, err := isolate.Remove(ctx, c.layout, c.git, c.task, OpIDFrom(ctx), c.repos)
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
			return nil, &CommandError{
				Kind:    contract.KindNotFound,
				Message: fmt.Sprintf("no isolate %q to remove", c.task),
				Help:    "create one with: wi isolate new <task> <repo>…",
			}
		}
		return nil, fmt.Errorf("isolate rm %q: %w", c.task, err)
	}

	repos := make([]contract.RepoResult, len(res.Repos))
	removed, blocked := 0, 0
	allNotMember := true
	for i, oc := range res.Repos {
		repos[i] = projectRemoveOutcome(oc)
		if oc.Removed {
			removed++
		} else {
			blocked++
			if !errors.Is(oc.Err, isolate.ErrRepoNotInIsolate) {
				allNotMember = false
			}
		}
	}

	// Every targeted repo reclaimed → a clean removed success.
	if blocked == 0 {
		return &Result{Action: contract.ActionRemoved, Repos: repos}, nil
	}

	// Some reclaimed, some not → DURABLE PARTIAL: re-running continues teardown.
	if removed > 0 {
		return &Result{Action: contract.ActionRemoved, Repos: repos},
			&CommandError{
				Kind:    contract.KindPartial,
				Action:  contract.ActionRemoved,
				Message: fmt.Sprintf("isolate %q partially removed — see repos[] for what is still blocked", c.task),
			}
	}

	// Nothing reclaimed. If the only reason is "these repos aren't members", that is
	// not_found; otherwise the isolate exists but refuses (orphan hard-block) → conflict.
	if allNotMember {
		return &Result{Action: contract.ActionNoop, Repos: repos},
			&CommandError{
				Kind:    contract.KindNotFound,
				Action:  contract.ActionNoop,
				Message: fmt.Sprintf("no targeted repo belongs to isolate %q", c.task),
				Help:    "list members with: wi resolve " + c.task,
			}
	}
	return &Result{Action: contract.ActionNoop, Repos: repos},
		&CommandError{
			Kind:    contract.KindConflict,
			Action:  contract.ActionNoop,
			Message: fmt.Sprintf("isolate %q not removed — see repos[] for the blocking orphan(s)", c.task),
			Help:    "inspect the worktree(s); wi will not auto-prune an unexplained orphan (DESIGN §7.1)",
		}
}

// projectRemoveOutcome maps one isolate.RemoveOutcome onto the wire RepoResult. A reclaimed
// repo is action=removed; a HARD BLOCK (Reason set) is action=noop carrying a per-repo
// conflict error sub-coded orphan_unexplained (the DESIGN §7.1 loud surface — the kind is
// conflict because the worktree's on-disk state conflicts with safe reclamation, whether it
// is unowned, dirty, or ahead of base); a repo that is not a member is not_found; any other
// per-repo fault is internal.
func projectRemoveOutcome(oc isolate.RemoveOutcome) contract.RepoResult {
	if oc.Removed {
		return contract.RepoResult{Repo: oc.Repo, Action: contract.ActionRemoved}
	}
	rr := contract.RepoResult{Repo: oc.Repo, Action: contract.ActionNoop}
	switch {
	case oc.Reason != "":
		rr.Error = &contract.Error{
			Kind:    contract.KindConflict,
			Code:    "orphan_unexplained",
			Message: oc.Reason,
			Repo:    oc.Repo,
		}
	case errors.Is(oc.Err, isolate.ErrRepoNotInIsolate):
		rr.Error = &contract.Error{
			Kind:    contract.KindNotFound,
			Message: oc.Err.Error(),
			Repo:    oc.Repo,
		}
	case oc.Err != nil:
		rr.Error = &contract.Error{
			Kind:    contract.KindInternal,
			Message: oc.Err.Error(),
			Repo:    oc.Repo,
		}
	}
	return rr
}
