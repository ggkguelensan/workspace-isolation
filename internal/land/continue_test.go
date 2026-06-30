package land_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/land"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// land.Continue is the forward dual of land.Abort over the SAME parked landstate record
// (DESIGN §7.2, HEAL-5 leaf 4): under the isolate-state:<task> lock it loads the durable
// landstate.TaskLand record and RE-ATTEMPTS LandRepo for every non-landed cell, landing
// those whose work now fast-forwards and re-parking those that still refuse, while
// carrying PhaseLanded cells through untouched.
//
// It DIVERGES from Run's stop-at-first-block (decision #CONTINUE-ATTEMPT-ALL): wi repos
// land onto INDEPENDENT bases with no modeled cross-repo dependency, so a residual block
// on one repo must not hold back another that can now land — continue maximizes recovery
// progress, the dual of abort processing every landed repo. Disposition (decision
// #CONTINUE-DISPOSE): continue KEEPS the record in BOTH outcomes (it never Delete's it) —
// a fully-completed continue reaches the IDENTICAL durable state as a clean land.Run
// (an all-landed record, still abortable), and a residual block keeps it parked for a
// further continue/abort. This is the deliberate ASYMMETRY with abort, whose terminal
// success state is no record at all. A still-blocking repo is a recorded refusal, NOT a
// Go error (Phase blocked, Err nil); the error return is reserved for failures that
// prevent the op at all (no record → landstate.ErrNoRecord, a held lock, an infra fault).
//
// Guard LAND-CONTINUE. Non-vacuity mutants (registered):
//   - make continue STOP at the first still-blocked repo (mirror Run's stop-at-first-block,
//     e.g. `break` after a non-landed cell) instead of attempting every non-landed cell →
//     TestContinueLandsUnblockedAndKeepsResidualBlocked RED (the landable web AFTER the
//     still-blocked api is never reached — absent from the result / its base never moves)
//     while the all-resolved TestContinueCompletesParkedLandAndKeepsRecord stays GREEN
//     (api lands first there, so there is no block to stop at) — two-sided, pinning
//     #CONTINUE-ATTEMPT-ALL;
//   - Delete the record on a fully-landed continue (copy abort's #ABORT-DISPOSE) →
//     TestContinueCompletesParkedLandAndKeepsRecord RED on the record-KEPT assertion only
//     (the base-advanced + landed-phase assertions stay GREEN) while the partial test
//     (Status blocked → never deletes) stays GREEN — two-sided, pinning #CONTINUE-DISPOSE.

func TestContinueCompletesParkedLandAndKeepsRecord(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	names := []string{"api", "web"}
	for _, n := range names {
		landCloneSSOT(t, env, l, g, ctx, n)
	}
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_iso", isoSpecs(names...)); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	// api commits work; a COMPETING land then diverges api's base so the first land parks
	// api blocked. web commits a landable tip the stop-at-first run never reaches.
	apiWt, _ := l.Isolate(task, "api")
	commitWork(t, env, apiWt, "feature.txt", "api work\n")
	apiSSOT, _ := l.Repo("api")
	other := filepath.Join(env.Root, "other-api")
	if err := g.AddWorktree(ctx, apiSSOT, other, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree(other): %v", err)
	}
	divergent := commitWork(t, env, other, "other.txt", "competing\n")
	env.Git(t, apiSSOT, "update-ref", "refs/heads/"+testenv.DefaultBranch, divergent)

	webWt, _ := l.Isolate(task, "web")
	webTip := commitWork(t, env, webWt, "feature.txt", "web work\n")

	parked, err := land.Run(ctx, l, g, task, "op_land", landSpecs(names...))
	if err != nil {
		t.Fatalf("land.Run: %v", err)
	}
	if parked.Status != land.StatusBlocked {
		t.Fatalf("precondition: parked land Status = %q, want blocked", parked.Status)
	}
	if api := findRepo(t, parked, "api"); api.Phase != landstate.PhaseBlocked {
		t.Fatalf("precondition: api Phase = %q, want blocked", api.Phase)
	}
	if web := findRepo(t, parked, "web"); web.Phase != landstate.PhasePending {
		t.Fatalf("precondition: web Phase = %q, want pending", web.Phase)
	}

	// The agent reconciles api: rebase its work onto the advanced base (here: reset the
	// isolate onto the divergent base tip and commit afresh) so api's work tip now
	// fast-forwards the base. web is untouched and still landable.
	env.Git(t, apiWt, "reset", "--hard", divergent)
	apiTip := commitWork(t, env, apiWt, "feature.txt", "api work rebased\n")

	res, err := land.Continue(ctx, l, g, task, "op_continue", landSpecs(names...))
	if err != nil {
		t.Fatalf("land.Continue: %v", err)
	}
	if res.Status != land.StatusLanded {
		t.Errorf("Status = %q, want %q (every repo now landed)", res.Status, land.StatusLanded)
	}
	for n, tip := range map[string]string{"api": apiTip, "web": webTip} {
		rr := findRepo(t, res, n)
		if rr.Phase != landstate.PhaseLanded {
			t.Errorf("%s Phase = %q, want landed", n, rr.Phase)
		}
		if rr.LandedSHA != tip {
			t.Errorf("%s LandedSHA = %q, want work tip %q", n, rr.LandedSHA, tip)
		}
		ssot, _ := l.Repo(n)
		if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != tip {
			t.Errorf("%s base after continue = %q, want work tip %q", n, got, tip)
		}
	}

	// Continue KEEPS the record (decision #CONTINUE-DISPOSE): a completed continue reaches
	// the same durable state as a fresh full land (record retained, every cell landed,
	// still abortable) — NOT abort's "no record" terminal state.
	rec, err := landstate.Load(l.LandDir(), task)
	if err != nil {
		t.Fatalf("record after a completed continue: want it KEPT, got %v", err)
	}
	for _, n := range names {
		if c := findCell(t, rec, n); c.Phase != landstate.PhaseLanded {
			t.Errorf("durable %s Phase = %q, want landed", n, c.Phase)
		}
	}
}

func TestContinueLandsUnblockedAndKeepsResidualBlocked(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	names := []string{"api", "web"}
	for _, n := range names {
		landCloneSSOT(t, env, l, g, ctx, n)
	}
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_iso", isoSpecs(names...)); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	apiWt, _ := l.Isolate(task, "api")
	commitWork(t, env, apiWt, "feature.txt", "api work\n")
	apiSSOT, _ := l.Repo("api")
	other := filepath.Join(env.Root, "other-api")
	if err := g.AddWorktree(ctx, apiSSOT, other, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree(other): %v", err)
	}
	divergent := commitWork(t, env, other, "other.txt", "competing\n")
	env.Git(t, apiSSOT, "update-ref", "refs/heads/"+testenv.DefaultBranch, divergent)

	webWt, _ := l.Isolate(task, "web")
	webTip := commitWork(t, env, webWt, "feature.txt", "web work\n")
	webSSOT, _ := l.Repo("web")

	if _, err := land.Run(ctx, l, g, task, "op_land", landSpecs(names...)); err != nil {
		t.Fatalf("land.Run: %v", err)
	}

	// api is left UNRECONCILED (still a non-fast-forward). Continue must STILL land web —
	// the repo the stop-at-first Run never reached — proving it attempts every non-landed
	// cell rather than stopping at api (decision #CONTINUE-ATTEMPT-ALL).
	res, err := land.Continue(ctx, l, g, task, "op_continue", landSpecs(names...))
	if err != nil {
		t.Fatalf("a residual block is a recorded refusal, not a Go error: %v", err)
	}
	if res.Status != land.StatusBlocked {
		t.Errorf("Status = %q, want %q (api still blocks)", res.Status, land.StatusBlocked)
	}

	// web base advanced to its work tip even though api blocks BEFORE it in record order.
	if got := env.Git(t, webSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != webTip {
		t.Errorf("web base after continue = %q, want advanced to %q (continue must land the unblocked repo past the block)", got, webTip)
	}
	web := findRepo(t, res, "web")
	if web.Phase != landstate.PhaseLanded {
		t.Errorf("web Phase = %q, want landed", web.Phase)
	}
	if web.LandedSHA != webTip {
		t.Errorf("web LandedSHA = %q, want %q", web.LandedSHA, webTip)
	}

	// api remains blocked, its base UNTOUCHED at the divergent tip (never force-moved).
	api := findRepo(t, res, "api")
	if api.Phase != landstate.PhaseBlocked {
		t.Errorf("api Phase = %q, want still blocked", api.Phase)
	}
	if got := env.Git(t, apiSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != divergent {
		t.Errorf("api base after continue = %q, want untouched at %q", got, divergent)
	}

	// The record is KEPT, reflecting web landed + api blocked, for a further continue/abort.
	rec, err := landstate.Load(l.LandDir(), task)
	if err != nil {
		t.Fatalf("record after a partial continue: want it kept, got %v", err)
	}
	if c := findCell(t, rec, "web"); c.Phase != landstate.PhaseLanded {
		t.Errorf("durable web Phase = %q, want landed", c.Phase)
	}
	if c := findCell(t, rec, "api"); c.Phase != landstate.PhaseBlocked {
		t.Errorf("durable api Phase = %q, want blocked", c.Phase)
	}
}

func TestContinueNoRecordIsErrNoRecord(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	landCloneSSOT(t, env, l, g, ctx, "api")
	if _, err := land.Continue(ctx, l, g, "never-landed", "op_continue", landSpecs("api")); !errors.Is(err, landstate.ErrNoRecord) {
		t.Errorf("Continue(no record): want ErrNoRecord, got %v", err)
	}
}
