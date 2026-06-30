package land_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/land"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// land.Preflight is the non-mutating validate-all gate `wi land --atomic` consults BEFORE
// the first pointer move (DESIGN §7.2, sub-unit 2 of the land-atomic capability). For each
// repo it resolves the work tip (worktree HEAD) + base tip and asks git.IsAncestor — would
// this work fast-forward onto its base? It writes NO backup ref, advances NO base, persists
// NO landstate record, and takes NO lock: a pure read the atomic orchestrator runs under its
// own already-held lock. ok is true IFF EVERY repo would land; a genuine infra fault (an
// unresolvable ref) is a Go error, distinct from a clean would-not-fast-forward (WouldLand
// false, not an error) — the same refusal/fault split LandRepo draws.
//
// The property that makes it WORTH having, distinct from v0 Run's stop-at-first-block: with
// a landable repo FIRST and a blocker SECOND, a sequential land would advance the first
// repo's base before discovering the second can't land (a partial). The all-or-nothing gate
// must detect the blocker while STILL having moved nothing.
//
// Guard LAND-PREFLIGHT. Non-vacuity mutants (registered):
//   - PRIMARY: drop the `if !ff { ok = false }` accumulation (ok stays true always) →
//     TestPreflightRefusesWhenAnyRepoBlocks RED on ok==false; the all-land test stays GREEN
//     (two-sided — the mutant fails exactly the blocking branch);
//   - ALTERNATE: hardcode WouldLand:true in the per-repo cell (ignore the IsAncestor result)
//     → the same test RED on the per-repo api.WouldLand==false assertion.

func findCheck(t *testing.T, checks []land.RepoPreflight, repo string) land.RepoPreflight {
	t.Helper()
	for _, c := range checks {
		if c.Repo == repo {
			return c
		}
	}
	t.Fatalf("repo %q absent from preflight checks", repo)
	return land.RepoPreflight{}
}

func TestPreflightAllReposWouldLand(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	names := []string{"api", "web"}
	for _, n := range names {
		landCloneSSOT(t, env, l, g, ctx, n)
	}
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_iso", isoSpecs(names...)); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	// Every repo commits a clean fast-forward work tip; record each base BEFORE preflight.
	baseBefore := map[string]string{}
	workTip := map[string]string{}
	for _, n := range names {
		ssot, _ := l.Repo(n)
		baseBefore[n] = env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
		wt, _ := l.Isolate(task, n)
		workTip[n] = commitWork(t, env, wt, "feature.txt", "work in "+n+"\n")
	}

	checks, ok, err := land.Preflight(ctx, g, l, task, landSpecs(names...))
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !ok {
		t.Errorf("ok = false, want true (every repo would fast-forward)")
	}
	for _, n := range names {
		c := findCheck(t, checks, n)
		if !c.WouldLand {
			t.Errorf("%s WouldLand = false, want true", n)
		}
		if c.WorkTip != workTip[n] {
			t.Errorf("%s WorkTip = %q, want %q", n, c.WorkTip, workTip[n])
		}
		if c.BaseTip != baseBefore[n] {
			t.Errorf("%s BaseTip = %q, want %q", n, c.BaseTip, baseBefore[n])
		}
	}

	// Preflight moved NOTHING: bases untouched, no backup anchored, no landstate written.
	assertNothingMoved(t, env, l, g, ctx, task, names, baseBefore)
}

func TestPreflightRefusesWhenAnyRepoBlocks(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	// "web" FIRST and landable, "api" SECOND and a blocker: a sequential land would advance
	// web's base before discovering api can't land. The atomic gate must catch api while
	// web's base is STILL untouched.
	names := []string{"web", "api"}
	for _, n := range names {
		landCloneSSOT(t, env, l, g, ctx, n)
	}
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_iso", isoSpecs(names...)); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	baseBefore := map[string]string{}
	for _, n := range names {
		ssot, _ := l.Repo(n)
		baseBefore[n] = env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
	}

	// web commits a perfectly landable work tip.
	webWt, _ := l.Isolate(task, "web")
	webWork := commitWork(t, env, webWt, "feature.txt", "web work\n")

	// api commits work, then a COMPETING land moves api's base to a divergent commit — api's
	// work tip is no longer a fast-forward.
	apiWt, _ := l.Isolate(task, "api")
	commitWork(t, env, apiWt, "feature.txt", "api work\n")
	apiSSOT, _ := l.Repo("api")
	other := filepath.Join(env.Root, "other-api")
	if err := g.AddWorktree(ctx, apiSSOT, other, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree(other): %v", err)
	}
	divergent := commitWork(t, env, other, "other.txt", "competing\n")
	env.Git(t, apiSSOT, "update-ref", "refs/heads/"+testenv.DefaultBranch, divergent)
	baseBefore["api"] = divergent // api's base is now the divergent tip

	checks, ok, err := land.Preflight(ctx, g, l, task, landSpecs(names...))
	if err != nil {
		t.Fatalf("Preflight on a non-ff must be a clean refusal, not an error: %v", err)
	}
	if ok {
		t.Errorf("ok = true, want false (api would not fast-forward)")
	}
	if web := findCheck(t, checks, "web"); !web.WouldLand {
		t.Errorf("web WouldLand = false, want true (web is a clean fast-forward)")
	} else if web.WorkTip != webWork {
		t.Errorf("web WorkTip = %q, want %q", web.WorkTip, webWork)
	}
	if api := findCheck(t, checks, "api"); api.WouldLand {
		t.Errorf("api WouldLand = true, want false (its base diverged)")
	}

	// The atomic property: even though web WOULD land and is checked FIRST, Preflight moved
	// NOTHING — web's base is untouched, no backup anchored, no landstate record exists.
	assertNothingMoved(t, env, l, g, ctx, task, names, baseBefore)
}

// assertNothingMoved verifies Preflight's purity: every repo's base ref is exactly as it was,
// no refs/wi/backup anchor was written, and no landstate record exists for the task.
func assertNothingMoved(t *testing.T, env *testenv.Env, l layout.Layout, g *git.Git, ctx context.Context, task string, names []string, baseBefore map[string]string) {
	t.Helper()
	for _, n := range names {
		ssot, _ := l.Repo(n)
		if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != baseBefore[n] {
			t.Errorf("%s base ref = %q, want it unchanged at %q (preflight must move nothing)", n, got, baseBefore[n])
		}
		if _, exists, err := g.BackupRefSHA(ctx, ssot, task, n); err != nil {
			t.Fatalf("%s BackupRefSHA: %v", n, err)
		} else if exists {
			t.Errorf("%s has a backup anchor, want none (preflight must write no backup ref)", n)
		}
	}
	if _, err := landstate.Load(l.LandDir(), task); err == nil {
		t.Errorf("a landstate record exists for %q, want none (preflight must persist nothing)", task)
	}
}
