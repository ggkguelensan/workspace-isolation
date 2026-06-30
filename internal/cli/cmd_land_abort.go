package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/ggkguelensan/workspace-isolation/internal/config"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/land"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// newLandAbortCommand is the `wi land abort <task>` factory — the mutating sibling of
// `wi land status`, the third HEAL-5 leaf (DESIGN §7.2). It takes exactly one safe <task>
// positional (the traversal check happens HERE so a bad task name is a clean usage refusal,
// not an opaque landstate error later) and binds task + layout + the git driver into a
// runnable Command (abort mutates base refs, so unlike status it needs git). The 2-token
// "land abort" registry key beats the 1-token "land" via Dispatch's longest match, so
// `wi land abort feat` routes here while `wi land feat <repo>…` still lands.
func newLandAbortCommand(l layout.Layout, g *git.Git, args []string) (Command, error) {
	if len(args) != 1 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi land abort <task>"}
	}
	task := args[0]
	if err := layout.ValidateSegment("task", task); err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	return &landAbortCmd{layout: l, git: g, task: task}, nil
}

// landAbortCmd is the seam between the abort domain core (land.Abort — the guarded
// base-rewind disposition, DESIGN §7.2) and the envelope contract. Run (a) pre-loads the
// parked record to learn which repos the land covers, mapping ErrNoRecord to a clean
// not_found (never landed, or already aborted); (b) resolves each of those repos' base from
// the manifest into a land.RepoSpec — the record carries shas, not base branch names, so the
// CLI resolves them exactly as `land` does (a missing manifest → not_found+`wi init`, an
// undeclared repo → not_found); (c) drives land.Abort under the minted op_id and maps its
// AbortResult.Status onto the return convention, mirroring landCmd's three-way map:
//   - AbortStatusAborted (every landed repo rewound, record discarded) → an action=removed
//     Result (exit 0); the isolate work still lives in the worktrees, only the bases moved back;
//   - AbortStatusBlocked with ≥1 repo rewound (a base advanced past its landed tip since the
//     land, so abort refused THAT repo but rewound the rest and KEPT the record) → the DURABLE
//     PARTIAL (result, *CommandError{partial, Action: removed}) — the rewinds are real progress,
//     a retry finishes once the base settles (exit 2);
//   - AbortStatusBlocked with NOTHING rewound → a full refusal *CommandError{conflict} (exit 4):
//     nothing was undone, newer work sits on top of the only landed repo;
//   - a held lock → lock_held (exit 6).
//
// This mirrors landCmd's mapping because the shapes are dual: a land's per-repo block is a
// non-fast-forward that needs a rebase; an abort's per-repo block is a base that advanced past
// the landed tip and needs the newer work reconciled — neither self-heals on a blind re-run, so
// "some progress" is a resumable partial and "no progress" is a conflict. A refused repo rides
// in repos[] as a per-repo conflict coded base_advanced (NOT Blocked[]: envelopeFor threads only
// Repos onto a failure envelope, so a non-zero exit must surface it there). It never assembles an
// envelope or chooses an exit code — the pipeline owns that.
type landAbortCmd struct {
	layout layout.Layout
	git    *git.Git
	task   string
}

func (c *landAbortCmd) Run(ctx context.Context) (*Result, error) {
	// Pre-load the record to learn which repos the parked land covers — the names we resolve
	// bases for (land.Abort takes specs; the domain core does not read the manifest). This
	// lockless pre-read is safe: landstate.Store renames atomically so a reader never tears,
	// and land.Abort re-loads under the isolate-state lock, which is the authoritative load.
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
				Help:    "re-declare it in wi.config.jsonc so its base is known, then retry the abort",
			}
		}
		specs = append(specs, land.RepoSpec{Name: r.Name, Base: r.Base})
	}

	res, err := land.Abort(ctx, c.layout, c.git, c.task, OpIDFrom(ctx), specs)
	if err != nil {
		// The record can vanish between the pre-load and Abort's locked load (a concurrent
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
		return nil, fmt.Errorf("land abort %q: %w", c.task, err)
	}

	repos := make([]contract.RepoResult, len(res.Repos))
	rewound := 0
	for i, ar := range res.Repos {
		repos[i] = projectAbortOutcome(ar)
		if ar.RestoredTo != "" {
			rewound++
		}
	}

	// Every landed repo rewound and the record was discarded → a clean abort (exit 0).
	if res.Status == land.AbortStatusAborted {
		return &Result{
			Action: contract.ActionRemoved,
			Repos:  repos,
			Next: []string{
				fmt.Sprintf("re-land when ready with: wi land %s <repo>…", c.task),
				fmt.Sprintf("or discard the isolate work with: wi isolate rm %s", c.task),
			},
		}, nil
	}

	// AbortStatusBlocked: at least one landed repo's base advanced past its landed tip, so
	// abort refused it (base untouched). Some rewound → durable progress, the record was kept →
	// a resumable partial (exit 2).
	if rewound > 0 {
		return &Result{Action: contract.ActionRemoved, Repos: repos},
			&CommandError{
				Kind:    contract.KindPartial,
				Action:  contract.ActionRemoved,
				Message: fmt.Sprintf("land abort %q partially rewound — see repos[] for the repo whose base advanced past its landed tip", c.task),
			}
	}

	// Nothing rewound: the only landed repo's base advanced, so NO base moved — a full refusal.
	return &Result{Action: contract.ActionNoop, Repos: repos},
		&CommandError{
			Kind:    contract.KindConflict,
			Action:  contract.ActionNoop,
			Message: fmt.Sprintf("land abort %q refused — the landed repo's base advanced past its landed tip; nothing rewound", c.task),
			Help:    "newer work was landed on top; reconcile it, then retry: wi land abort " + c.task,
		}
}

// noRecord is the shared not_found refusal for "this task has no parked land" — emitted both
// at the pre-load and (defensively) if the record vanishes before Abort's locked re-load.
func (c *landAbortCmd) noRecord() *CommandError {
	return &CommandError{
		Kind:    contract.KindNotFound,
		Message: fmt.Sprintf("no parked land for %q", c.task),
		Help:    "it was never landed, or the land was already aborted; check the task name",
	}
}

// projectAbortOutcome maps one land.AbortRepoResult onto the wire RepoResult. A rewound repo
// (RestoredTo set) is action=removed carrying the restored pre-land sha its base now points at,
// with stage echoing its post-abort phase (pending — the work still lives in the isolate). A
// refused repo (Err set — always a *git.StaleBaseRefError per land.Abort's contract) is
// action=noop carrying a per-repo conflict coded base_advanced: its base moved past the landed
// tip since the land, so abort refused it to avoid discarding newer work — the agent reconciles
// that work, then retries. A repo the land never advanced (still pending/blocked, neither set)
// is a plain action=noop with no error. Mirror/Branch stay empty for v1, matching projectLandOutcome.
func projectAbortOutcome(ar land.AbortRepoResult) contract.RepoResult {
	if ar.RestoredTo != "" {
		return contract.RepoResult{
			Repo:   ar.Repo,
			Action: contract.ActionRemoved,
			SHA:    ar.RestoredTo,
			Stage:  string(ar.Phase),
		}
	}
	out := contract.RepoResult{Repo: ar.Repo, Action: contract.ActionNoop, Stage: string(ar.Phase)}
	if ar.Err != nil {
		out.Error = &contract.Error{
			Kind:    contract.KindConflict,
			Code:    "base_advanced",
			Message: fmt.Sprintf("%s: base %q advanced past the landed tip since the land — abort refused it to avoid discarding newer work", ar.Repo, ar.Base),
			Repo:    ar.Repo,
		}
	}
	return out
}
