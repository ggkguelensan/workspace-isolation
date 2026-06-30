package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/ggkguelensan/workspace-isolation/internal/baseref"
	"github.com/ggkguelensan/workspace-isolation/internal/config"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/land"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// newLandContinueCommand is the `wi land continue <task>` factory — the forward sibling of
// `wi land abort` and the fourth HEAL-5 leaf (DESIGN §7.2). Like abort it takes exactly one
// safe <task> positional (the traversal check happens HERE so a bad task name is a clean
// usage refusal, not an opaque landstate error later) and binds task + layout + the git
// driver into a runnable Command (continue mutates base refs, so like land/abort it needs
// git). The 2-token "land continue" registry key beats the 1-token "land" via Dispatch's
// longest match, so `wi land continue feat` routes here while `wi land feat <repo>…` lands.
func newLandContinueCommand(l layout.Layout, g *git.Git, args []string) (Command, error) {
	if len(args) != 1 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi land continue <task>"}
	}
	task := args[0]
	if err := layout.ValidateSegment("task", task); err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	return &landContinueCmd{layout: l, git: g, task: task}, nil
}

// landContinueCmd is the seam between the continue domain core (land.Continue — the
// attempt-all, keep-the-record resume, DESIGN §7.2) and the envelope contract. Run (a)
// pre-loads the parked record to learn which repos the land covers, mapping ErrNoRecord to a
// clean not_found (never landed, or already torn down); (b) resolves each of those repos' base
// from the manifest into a land.RepoSpec — the record carries shas, not base branch names, so
// the CLI resolves them exactly as `land`/`land abort` do (a missing manifest →
// not_found+`wi init`, an undeclared repo → not_found); (c) drives land.Continue under the
// minted op_id and maps its Result.Status / landed-count onto the return convention, REUSING
// projectLandOutcome and MIRRORING landCmd's three-way map:
//   - StatusLanded (every repo now landed) → an action=landed Result (exit 0); the record is
//     KEPT by land.Continue (#CONTINUE-DISPOSE) — a completed continue reaches the same
//     abortable state a clean land leaves, NOT abort's "no record" terminal state;
//   - StatusBlocked with ≥1 repo landed (this run or carried through) → the DURABLE PARTIAL
//     (result, *CommandError{partial, Action: landed}) — the landed bases are real progress, a
//     further continue resumes the rest once the blocker is reconciled (exit 2);
//   - StatusBlocked with NOTHING landed → a full refusal *CommandError{conflict} (exit 4):
//     every repo still refuses, no base advanced — the agent rebases the blockers, then retries;
//   - a held lock → lock_held (exit 6).
//
// This mirrors landCmd's mapping because the shapes are dual: both surface "some progress is a
// resumable partial, no progress is a conflict" over a stop-/attempt-loop of per-repo
// fast-forwards. A still-blocked repo rides in repos[] as a per-repo conflict coded
// non_fast_forward — the SAME shape landCmd gives a parked block (NOT Blocked[]: envelopeFor
// threads only Repos onto a failure envelope, so a non-zero exit must surface it there). It
// never assembles an envelope or chooses an exit code — the pipeline owns that.
type landContinueCmd struct {
	layout layout.Layout
	git    *git.Git
	task   string
}

func (c *landContinueCmd) Run(ctx context.Context) (*Result, error) {
	// Pre-load the record to learn which repos the parked land covers — the names we resolve
	// bases for (land.Continue takes specs; the domain core does not read the manifest). This
	// lockless pre-read is safe: landstate.Store renames atomically so a reader never tears, and
	// land.Continue re-loads under the isolate-state lock, which is the authoritative load.
	rec, err := landstate.Load(c.layout.LandDir(), c.task)
	if errors.Is(err, landstate.ErrNoRecord) {
		return nil, c.noRecord()
	}
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(c.layout.Config())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &CommandError{
				Kind:    contract.KindNotFound,
				Message: fmt.Sprintf("no wi workspace here: %s not found", c.layout.Config()),
				Help:    "create one with: wi init",
			}
		}
		// A malformed manifest is user-fixable input, not a wi bug → usage (exit 64).
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}

	specs := make([]land.RepoSpec, 0, len(rec.Repos))
	for _, rl := range rec.Repos {
		r, ok := cfg.Lookup(rl.Repo)
		if !ok {
			return nil, &CommandError{
				Kind:    contract.KindNotFound,
				Message: fmt.Sprintf("repo %q in the parked land for %q is no longer declared in the manifest", rl.Repo, c.task),
				Repo:    rl.Repo,
				Help:    "re-declare it in wi.config.jsonc so its base is known, then retry the continue",
			}
		}
		specs = append(specs, land.RepoSpec{Name: r.Name, Base: baseref.Resolve(ctx, c.git, c.layout, r.Name, r.Base)})
	}

	res, err := land.Continue(ctx, c.layout, c.git, c.task, OpIDFrom(ctx), specs)
	if err != nil {
		// The record can vanish between the pre-load and Continue's locked load (a concurrent
		// abort) — still a clean not_found, not an internal error.
		if errors.Is(err, landstate.ErrNoRecord) {
			return nil, c.noRecord()
		}
		var he *lock.HeldError
		if errors.As(err, &he) {
			return nil, &CommandError{
				Kind:    contract.KindLockHeld,
				Message: fmt.Sprintf("isolate %q is busy: %v", c.task, err),
				Help:    "another wi operation holds this task's lock; retry when it finishes",
			}
		}
		return nil, fmt.Errorf("land continue %q: %w", c.task, err)
	}

	repos := make([]contract.RepoResult, len(res.Repos))
	landed := 0
	for i, rr := range res.Repos {
		repos[i] = projectLandOutcome(rr)
		if rr.Phase == landstate.PhaseLanded {
			landed++
		}
	}

	// Every repo landed (newly or carried through) → a clean landed success. land.Continue
	// KEPT the record (#CONTINUE-DISPOSE), so the same `wi isolate rm` / `wi land abort`
	// follow-ups a fresh land offers still apply.
	if res.Status == land.StatusLanded {
		return &Result{
			Action: contract.ActionLanded,
			Repos:  repos,
			Next:   []string{fmt.Sprintf("tear down the isolate with: wi isolate rm %s", c.task)},
		}, nil
	}

	// StatusBlocked: at least one repo still refuses. Some landed → durable progress, the record
	// was kept → a resumable partial (exit 2).
	if landed > 0 {
		return &Result{Action: contract.ActionLanded, Repos: repos},
			&CommandError{
				Kind:    contract.KindPartial,
				Action:  contract.ActionLanded,
				Message: fmt.Sprintf("land continue %q partially landed — see repos[] for what is still blocked", c.task),
			}
	}

	// Nothing landed: every repo still refuses, so NO base advanced — a full refusal.
	return &Result{Action: contract.ActionNoop, Repos: repos},
		&CommandError{
			Kind:    contract.KindConflict,
			Action:  contract.ActionNoop,
			Message: fmt.Sprintf("land continue %q refused — every repo still blocks; no base advanced", c.task),
			Help:    "rebase the blocked repos' work onto their bases, then retry: wi land continue " + c.task,
		}
}

// noRecord is the shared not_found refusal for "this task has no parked land" — emitted both at
// the pre-load and (defensively) if the record vanishes before Continue's locked re-load.
func (c *landContinueCmd) noRecord() *CommandError {
	return &CommandError{
		Kind:    contract.KindNotFound,
		Message: fmt.Sprintf("no parked land for %q", c.task),
		Help:    "it was never landed, or the land was already torn down; check the task name",
	}
}
