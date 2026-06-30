package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/config"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/land"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// newLandCommand is the `wi land [--atomic] <task> <repo>…` factory: it parses the optional
// --atomic flag out of args (the FIRST flag any land form carries; parseGlobals strips only
// --dry-run/--format, so --atomic arrives here in args), then validates the remaining
// positionals (a safe <task> segment + at least one <repo>) and binds task/repos/atomic plus
// the layout and git driver into a runnable Command. --atomic is accepted in any position; an
// UNKNOWN --flag is a clean usage refusal rather than a silent positional (mirrors
// parseStateCasArgs). The traversal check on <task> happens HERE so a bad task name is a clean
// usage refusal; repo names are checked against the manifest in Run (an undeclared one is
// not_found, not usage — the operand is well-formed, it just names nothing wi manages).
// Symmetric with `isolate new`'s factory.
func newLandCommand(l layout.Layout, g *git.Git, args []string) (Command, error) {
	atomic := false
	positionals := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a == "--atomic":
			atomic = true
		case strings.HasPrefix(a, "--"):
			return nil, &CommandError{Kind: contract.KindUsage, Message: fmt.Sprintf("unknown flag %q; usage: wi land [--atomic] <task> <repo>…", a)}
		default:
			positionals = append(positionals, a)
		}
	}
	if len(positionals) < 2 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi land [--atomic] <task> <repo>…"}
	}
	task := positionals[0]
	if err := layout.ValidateSegment("task", task); err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	return &landCmd{layout: l, git: g, task: task, repos: positionals[1:], atomic: atomic}, nil
}

// landCmd is the seam between the land domain core (land.RunJournaled — the journaled,
// durable, stop-at-first-block orchestrator, DESIGN §7.2/§7.4) and the envelope contract.
// Run (a) loads the manifest and resolves every requested repo to a land.RepoSpec — a
// missing manifest is not_found (+`wi init`), an undeclared repo is not_found, a malformed
// manifest is usage (user-fixable input, exit 64, NOT a wi bug); (b) reads the minted op_id
// from the context so the durable landstate record + op-journal carry the SAME id the
// envelope reports (CTX-OPID); (c) drives RunJournaled and maps its Status onto the return
// convention (decision #LD, recorded PROGRESS.md):
//   - StatusLanded (every repo's work fast-forwarded onto its base) → a landed Result (exit 0);
//   - mixed (≥1 landed AND the run then blocked) → the DURABLE PARTIAL (result,
//     *CommandError{partial, Action: landed}) carrying per-repo detail (decision #D) —
//     bases already advanced, `land continue` (HEAL-5) resumes the rest, so it is resumable,
//     not a clean refusal (exit 2);
//   - nothing landed, the first repo blocked → a full refusal *CommandError{conflict} (exit
//     4): NO base advanced, the on-disk work conflicts with a clean fast-forward — the agent
//     must rebase the blocked repo onto its base, then retry;
//   - a held lock → lock_held (exit 6).
//
// This mirrors `isolate rm`'s three-way mapping (complete / durable-partial / full-conflict),
// the right analog: a non-fast-forward block needs a rebase to clear, like an rm orphan needs
// the worktree resolved — neither self-heals on a blind re-run. (`isolate new`, by contrast,
// flattens any non-complete to a plain partial because its blocks are independent per-repo
// materialization faults, not a stop-the-world refusal.) Blocked repos ride in repos[] as
// per-repo Errors (a clean non-ff → conflict coded non_fast_forward; an infra fault →
// internal), NOT Blocked[]: envelopeFor threads only Repos/Warnings/Next onto a failure
// envelope, so a non-zero exit must surface them in repos[]. It never assembles an envelope
// or chooses an exit code — the pipeline owns that.
//
// With --atomic set (the land-atomic capability, DESIGN §7.2), Run consults land.Preflight —
// the non-mutating validate-all gate — BEFORE the first pointer move: if ANY repo would not
// fast-forward it refuses the WHOLE op (conflict, exit 4) having advanced no base, instead of
// the default stop-at-first-block march that lands the repos before the blocker. The recorded
// ruling (#ATOMIC-1) is pre-flight-then-normal-RunJournaled: Preflight takes no lock, but the
// check→land window is covered by RunJournaled's own isolate-state:<task> lock against a
// concurrent wi, and a racing EXTERNAL git mutation is still caught safely by LandRepo's own
// ff-refusal (it parks that repo; the base is never forced) — so the all-or-nothing guarantee
// holds in the uncontended single-agent case wi targets, with a safe degrade under a race.
type landCmd struct {
	layout layout.Layout
	git    *git.Git
	task   string
	repos  []string
	atomic bool
}

func (c *landCmd) Run(ctx context.Context) (*Result, error) {
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

	specs := make([]land.RepoSpec, 0, len(c.repos))
	for _, name := range c.repos {
		r, ok := cfg.Lookup(name)
		if !ok {
			return nil, &CommandError{
				Kind:    contract.KindNotFound,
				Message: fmt.Sprintf("repo %q is not declared in the manifest", name),
				Repo:    name,
				Help:    "declare it in wi.config.jsonc, or check the name",
			}
		}
		specs = append(specs, land.RepoSpec{Name: r.Name, Base: r.Base})
	}

	// --atomic: validate ALL repos would fast-forward before moving anything. If any blocks,
	// refuse the whole op (conflict) with NO base advanced — see the type doc for the lock
	// reasoning behind pre-flight-then-RunJournaled (#ATOMIC-1).
	if c.atomic {
		checks, ok, perr := land.Preflight(ctx, c.git, c.layout, c.task, specs)
		if perr != nil {
			return nil, fmt.Errorf("land %q --atomic preflight: %w", c.task, perr)
		}
		if !ok {
			repos := make([]contract.RepoResult, len(checks))
			for i, ck := range checks {
				repos[i] = projectPreflight(ck)
			}
			return &Result{Action: contract.ActionNoop, Repos: repos},
				&CommandError{
					Kind:    contract.KindConflict,
					Action:  contract.ActionNoop,
					Message: fmt.Sprintf("land %q --atomic refused — at least one repo would not fast-forward; NO base advanced", c.task),
					Help:    "rebase the blocked repos onto their bases, then retry: wi land --atomic " + c.task,
				}
		}
	}

	res, err := land.RunJournaled(ctx, c.layout, c.git, c.task, OpIDFrom(ctx), specs)
	if err != nil {
		var he *lock.HeldError
		if errors.As(err, &he) {
			return nil, &CommandError{
				Kind:    contract.KindLockHeld,
				Message: fmt.Sprintf("isolate %q is busy: %v", c.task, err),
				Help:    "another wi operation holds this task's lock; retry when it finishes",
			}
		}
		return nil, fmt.Errorf("land %q: %w", c.task, err)
	}

	repos := make([]contract.RepoResult, len(res.Repos))
	landed := 0
	for i, rr := range res.Repos {
		repos[i] = projectLandOutcome(rr)
		if rr.Phase == landstate.PhaseLanded {
			landed++
		}
	}

	// Every repo's work fast-forwarded onto its base → a clean landed success.
	if res.Status == land.StatusLanded {
		return &Result{
			Action: contract.ActionLanded,
			Repos:  repos,
			Next:   []string{fmt.Sprintf("tear down the isolate with: wi isolate rm %s", c.task)},
		}, nil
	}

	// StatusBlocked: the run stopped at the first repo that refused or faulted.
	// Some landed before it → DURABLE PARTIAL (the moved bases are real progress;
	// `land continue` resumes the rest).
	if landed > 0 {
		return &Result{Action: contract.ActionLanded, Repos: repos},
			&CommandError{
				Kind:    contract.KindPartial,
				Action:  contract.ActionLanded,
				Message: fmt.Sprintf("land %q partially landed — see repos[] for what is still blocked", c.task),
			}
	}

	// Nothing landed: the first repo blocked, so NO base advanced — a full refusal.
	return &Result{Action: contract.ActionNoop, Repos: repos},
		&CommandError{
			Kind:    contract.KindConflict,
			Action:  contract.ActionNoop,
			Message: fmt.Sprintf("land %q refused — no base advanced; see repos[] for the blocking repo", c.task),
			Help:    "rebase the blocked repo's work onto its base, then retry: wi land " + c.task,
		}
}

// projectLandOutcome maps one land.RepoResult onto the wire RepoResult. A landed repo is
// action=landed carrying the work tip its base advanced to (LandedSHA); a PARKED block is
// action=noop with a per-repo error — a clean non-fast-forward (Err nil) is a conflict
// sub-coded non_fast_forward so the agent knows to rebase, an infra fault (Err set) is
// internal; a repo the run never reached (PhasePending, after an earlier block) is a plain
// action=noop with no error. Stage echoes the durable landstate phase so the agent sees the
// per-repo lifecycle position. Mirror/Branch stay empty for v1.
func projectLandOutcome(rr land.RepoResult) contract.RepoResult {
	if rr.Phase == landstate.PhaseLanded {
		return contract.RepoResult{
			Repo:   rr.Repo,
			Action: contract.ActionLanded,
			SHA:    rr.LandedSHA,
			Stage:  string(rr.Phase),
		}
	}
	out := contract.RepoResult{Repo: rr.Repo, Action: contract.ActionNoop, Stage: string(rr.Phase)}
	if rr.Phase == landstate.PhaseBlocked {
		if rr.Err != nil {
			out.Error = &contract.Error{
				Kind:    contract.KindInternal,
				Message: rr.Err.Error(),
				Repo:    rr.Repo,
			}
		} else {
			out.Error = &contract.Error{
				Kind:    contract.KindConflict,
				Code:    "non_fast_forward",
				Message: fmt.Sprintf("%s: work tip is not a fast-forward of base %q — rebase onto it, then retry", rr.Repo, rr.Base),
				Repo:    rr.Repo,
			}
		}
	}
	return out
}

// projectPreflight maps one land.RepoPreflight (a non-mutating --atomic pre-check) onto the
// wire RepoResult for an atomic refusal. The op was refused before the first pointer move, so
// EVERY repo is action=noop (nothing landed); a repo that would NOT fast-forward carries a
// per-repo conflict coded non_fast_forward — the SAME shape projectLandOutcome gives a parked
// non-ff block, so an agent reads an --atomic refusal exactly like a stop-at-first-block one.
// A repo that WOULD have landed carries no error: it is blameless, the op refused on another
// repo's account.
func projectPreflight(ck land.RepoPreflight) contract.RepoResult {
	out := contract.RepoResult{Repo: ck.Repo, Action: contract.ActionNoop}
	if !ck.WouldLand {
		out.Error = &contract.Error{
			Kind:    contract.KindConflict,
			Code:    "non_fast_forward",
			Message: fmt.Sprintf("%s: work tip is not a fast-forward of base %q — rebase onto it, then retry", ck.Repo, ck.Base),
			Repo:    ck.Repo,
		}
	}
	return out
}
