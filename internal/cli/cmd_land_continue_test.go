package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard CMD-LAND-CONTINUE: `wi land continue <task>` is the forward sibling of `wi land abort`
// and the fourth HEAL-5 leaf (DESIGN §7.2) — it resumes a parked land by re-attempting each
// non-landed repo and landing those whose isolate work now fast-forwards its base, carrying
// already-landed repos through. The land/landstate guards already prove the per-repo land
// safety (ff-only advance, backup-before-move, non-ff → parked block) and land.Continue's
// guard (LAND-CONTINUE) proves the attempt-all + keep-record disposition; the handler's OWN
// responsibilities, untested there, are: (1) take exactly one safe <task> positional
// (traversal rejected at the factory as usage); (2) learn the parked land's repos by
// pre-loading the record, then resolve each base from the manifest into land.RepoSpec (an
// undeclared repo → not_found, a missing manifest → not_found+`wi init`); (3) the
// Result.Status/landed-count → envelope/return three-way map, REUSING projectLandOutcome and
// MIRRORING landCmd's (it NEVER assembles an envelope or picks an exit code):
//   - StatusLanded (every repo now landed) → a landed Result (exit 0); the record is KEPT
//     (decision #CONTINUE-DISPOSE) — a completed continue is the same abortable state a clean
//     land leaves, NOT abort's "no record" terminal state;
//   - StatusBlocked with ≥1 repo landed this run (or carried) → the DURABLE PARTIAL (result,
//     *CommandError{partial, Action: landed}) — the landed bases are real progress, a further
//     continue resumes the rest once reconciled (exit 2);
//   - StatusBlocked with NOTHING landed → a full refusal *CommandError{conflict} (exit 4):
//     every repo still refuses, no base advanced;
//   - ErrNoRecord (never landed / already torn down) → not_found; a held lock → lock_held.
// A still-blocked repo rides in repos[] as a per-repo conflict coded non_fast_forward (NOT
// Blocked[]: envelopeFor threads only Repos onto a failure envelope, so a non-zero exit must
// surface it there) — the SAME shape landCmd gives a parked block.
//
// Non-vacuity mutant (registered): in landContinueCmd.Run, on the blocked-with-landings
// outcome return (result, nil) instead of (result, *CommandError{partial}) → a partial
// continue mis-reported as a clean success → TestLandContinuePartialKeepsResidualBlocked RED
// (want *cli.CommandError{partial}, got nil) while the completes (exit 0) and all-blocked
// (exit 4) tests stay GREEN (two-sided). Alternate: map the all-blocked (nothing landed)
// outcome to KindPartial instead of KindConflict → TestLandContinueAllBlockedIsConflict RED
// (a nothing-changed refusal must be exit 4, not 2) while the partial test stays GREEN. Both
// reverted to byte-identical (cached) GREEN.

func landContinueFactory(t *testing.T, l layout.Layout, g *git.Git) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l, Git: g})["land continue"]
	if !ok {
		t.Fatal("BuildRegistry has no \"land continue\" factory")
	}
	return f
}

// parkLand drives the real `wi land <task> <repos…>` to produce a PARKED record + the bases
// it advanced — the blocked state a continue resumes. Unlike runLand it TOLERATES the
// partial/conflict *cli.CommandError a blocked land returns (the block IS the point); only a
// non-CommandError (genuine internal) failure fatals.
func parkLand(t *testing.T, l layout.Layout, g *git.Git, ctx context.Context, task string, repos ...string) {
	t.Helper()
	args := append([]string{task}, repos...)
	cmd, err := landFactory(t, l, g)(args)
	if err != nil {
		t.Fatalf("land factory: %v", err)
	}
	_, err = cmd.Run(cli.WithOpID(ctx, "op_land_setup"))
	var ce *cli.CommandError
	if err != nil && !errors.As(err, &ce) {
		t.Fatalf("park land setup: unexpected non-CommandError %v", err)
	}
}

// Completes: api lands, web's base diverged so the land parks web blocked (api=landed,
// web=blocked). The agent reconciles web (rebases its work onto the advanced base); continue
// then lands web too → every repo landed → a landed Result (exit 0). The record is KEPT
// (#CONTINUE-DISPOSE) — the completed continue reaches the same abortable state a clean land
// leaves, NOT abort's "no record".
func TestLandContinueCompletesParkedLandKeepsRecord(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "api", "web")
	materializeIsolate(t, l, g, ctx, "feat", "api", "web")
	landWork(t, env, l, "feat", "api")
	landWork(t, env, l, "feat", "web")
	divergeBase(t, env, l, g, ctx, "web") // web parks blocked; api (listed first) lands

	parkLand(t, l, g, ctx, "feat", "api", "web")

	// Precondition: api landed, web blocked.
	if rec, err := landstate.Load(l.LandDir(), "feat"); err != nil {
		t.Fatalf("load parked record: %v", err)
	} else {
		if c := findRecCell(t, rec, "api"); c.Phase != landstate.PhaseLanded {
			t.Fatalf("precondition: api phase = %q, want landed", c.Phase)
		}
		if c := findRecCell(t, rec, "web"); c.Phase != landstate.PhaseBlocked {
			t.Fatalf("precondition: web phase = %q, want blocked", c.Phase)
		}
	}

	// Reconcile web: rebase its isolate work onto the advanced base (reset onto the divergent
	// tip the base now points at, then commit afresh) so web's work tip fast-forwards the base.
	webWt, _ := l.Isolate("feat", "web")
	webSSOT, _ := l.Repo("web")
	divergent := env.Git(t, webSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
	env.Git(t, webWt, "reset", "--hard", divergent)
	env.Git(t, webWt, "commit", "--allow-empty", "-m", "web work rebased onto advanced base")
	webTip := env.Git(t, webWt, "rev-parse", "HEAD")

	cmd, err := landContinueFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_continue_cli"))
	if err != nil {
		t.Fatalf("Run (completes): unexpected error %v", err)
	}
	if res.Action != contract.ActionLanded {
		t.Errorf("Action = %q, want %q (every repo now landed)", res.Action, contract.ActionLanded)
	}

	// api was carried through landed; web landed this run at its reconciled work tip.
	api := landOutcome(t, res, "api")
	if api.Action != contract.ActionLanded || api.Error != nil {
		t.Errorf("api outcome = %+v, want landed/no-error (carried through)", api)
	}
	web := landOutcome(t, res, "web")
	if web.Action != contract.ActionLanded || web.Error != nil {
		t.Errorf("web outcome = %+v, want landed/no-error", web)
	}
	if web.SHA != webTip {
		t.Errorf("web landed sha = %q, want reconciled work tip %q", web.SHA, webTip)
	}
	if got := env.Git(t, webSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != webTip {
		t.Errorf("web base after continue = %q, want advanced to work tip %q", got, webTip)
	}

	// The record is KEPT, every cell landed (#CONTINUE-DISPOSE).
	rec, err := landstate.Load(l.LandDir(), "feat")
	if err != nil {
		t.Fatalf("record after a completed continue: want it KEPT, got %v", err)
	}
	for _, repo := range []string{"api", "web"} {
		if c := findRecCell(t, rec, repo); c.Phase != landstate.PhaseLanded {
			t.Errorf("durable %s phase = %q, want landed", repo, c.Phase)
		}
	}
}

// Durable partial: api's base diverged and is NOT reconciled; web is landable but the land's
// stop-at-first-block march never reached it (api blocks first → nothing landed, both parked).
// Continue attempts EVERY non-landed repo (#CONTINUE-ATTEMPT-ALL): web lands, api still
// refuses → ≥1 landed AND ≥1 blocked → a DURABLE PARTIAL (exit 2), result kept, record
// retained, api riding in repos[] as a conflict coded non_fast_forward.
func TestLandContinuePartialKeepsResidualBlocked(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "api", "web")
	materializeIsolate(t, l, g, ctx, "feat", "api", "web")
	landWork(t, env, l, "feat", "api")
	landWork(t, env, l, "feat", "web")
	divergeBase(t, env, l, g, ctx, "api") // api blocks FIRST → land parks both, nothing landed

	apiSSOT, _ := l.Repo("api")
	apiDiverged := env.Git(t, apiSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
	webWt, _ := l.Isolate("feat", "web")
	webTip := env.Git(t, webWt, "rev-parse", "HEAD")

	parkLand(t, l, g, ctx, "feat", "api", "web")

	cmd, err := landContinueFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_continue_cli"))

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("partial continue: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindPartial {
		t.Errorf("Kind = %q, want %q (web landed = durable progress)", ce.Kind, contract.KindPartial)
	}
	if ce.Action != contract.ActionLanded {
		t.Errorf("partial CommandError.Action = %q, want %q (the verb in flight)", ce.Action, contract.ActionLanded)
	}
	if res == nil {
		t.Fatal("a durable partial MUST carry a result alongside the error")
	}

	// web landed past the still-blocked api (proving attempt-all, not stop-at-first-block).
	web := landOutcome(t, res, "web")
	if web.Action != contract.ActionLanded || web.Error != nil {
		t.Errorf("web outcome = %+v, want landed/no-error", web)
	}
	webSSOT, _ := l.Repo("web")
	if got := env.Git(t, webSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != webTip {
		t.Errorf("web base after continue = %q, want advanced to %q", got, webTip)
	}
	// api refused — its base UNTOUCHED at the divergent tip, carrying a conflict non_fast_forward.
	api := landOutcome(t, res, "api")
	if api.Error == nil {
		t.Fatalf("api (non-ff) must carry a per-repo error, got %+v", api)
	}
	if api.Error.Kind != contract.KindConflict || api.Error.Code != "non_fast_forward" {
		t.Errorf("api error = %+v, want conflict coded non_fast_forward", api.Error)
	}
	if got := env.Git(t, apiSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != apiDiverged {
		t.Errorf("api base after continue = %q, want UNTOUCHED at %q", got, apiDiverged)
	}
	// The record is KEPT, reflecting web landed + api blocked, for a further continue/abort.
	rec, err := landstate.Load(l.LandDir(), "feat")
	if err != nil {
		t.Fatalf("record after a partial continue: want it kept, got %v", err)
	}
	if c := findRecCell(t, rec, "web"); c.Phase != landstate.PhaseLanded {
		t.Errorf("durable web phase = %q, want landed", c.Phase)
	}
	if c := findRecCell(t, rec, "api"); c.Phase != landstate.PhaseBlocked {
		t.Errorf("durable api phase = %q, want blocked", c.Phase)
	}
}

// Nothing landed: the sole repo's base diverged and stays unreconciled, so the parked land
// blocked it and continue still cannot land it → NO base advanced → a full conflict refusal
// (exit 4), NOT partial. The blocked repo still rides in repos[].
func TestLandContinueAllBlockedIsConflict(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "web")
	materializeIsolate(t, l, g, ctx, "feat", "web")
	landWork(t, env, l, "feat", "web")
	divergeBase(t, env, l, g, ctx, "web")

	parkLand(t, l, g, ctx, "feat", "web")

	cmd, err := landContinueFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_continue_cli"))

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("all-blocked: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindConflict {
		t.Errorf("Kind = %q, want %q (nothing landed is a full refusal)", ce.Kind, contract.KindConflict)
	}
	if res == nil || landOutcome(t, res, "web").Error == nil {
		t.Errorf("the blocked repo must still ride in repos[], got res=%+v", res)
	}
}

// A task with no parked land (never landed, or already torn down) is a clean not_found
// refusal, not an internal error — the record pre-load fails with ErrNoRecord before any
// mutation, mirroring `land abort`.
func TestLandContinueNoRecordIsNotFound(t *testing.T) {
	l := bootstrappedLayout(t)
	cmd, err := landContinueFactory(t, l, git.New(gitexec.New()))([]string{"ghost"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(context.Background(), "op_continue_cli"))
	if res != nil {
		t.Errorf("a task with no parked land must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindNotFound {
		t.Errorf("Kind = %q, want %q (no parked land is not_found, not internal)", ce.Kind, contract.KindNotFound)
	}
	if !contains(ce.Message, "ghost") {
		t.Errorf("not_found message should name the task, got %q", ce.Message)
	}
}

func TestLandContinueFactoryValidatesArgs(t *testing.T) {
	l := bootstrappedLayout(t)
	f := landContinueFactory(t, l, git.New(gitexec.New()))

	for _, args := range [][]string{nil, {}, {"a", "b"}} {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("args %v: want kind=usage, got %v", args, err)
		}
	}
	if _, err := f([]string{"../evil"}); !isUsage(err) {
		t.Errorf("traversing task name: want kind=usage, got %v", err)
	}
	cmd, err := f([]string{"feat"})
	if err != nil || cmd == nil {
		t.Errorf("one safe task arg must build a Command, got cmd=%v err=%v", cmd, err)
	}
}
