package recovery_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/recovery"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard HEAL-CRASH-RECOVER (dispatcher limb) — the per-kind Finisher the offline
// roll-forward executor (sub-unit 3c) injects (HEAL-4 sub-unit 3d-iii). journal.Recover
// takes an injected journal.Finisher so the journal package stays free of isolate/land
// deps (no import cycle); recovery.Finisher supplies it, routing each rolled-forward op
// by Kind to the domain that completes it. Today only isolate-rm journals, so the live
// routes are: isolate_rm → isolate.FinishRemove; every other kind → error (journal LEFT
// for retry, surfaced in the report — never silently discard an op recovery can't
// complete, the conservative posture of HEAL-4).
//
// Non-vacuity (registered as the HEAL-CRASH-RECOVER dispatcher-limb pair):
//   - (primary — real routing) route isolate_rm to the error default (or stub the arm
//     `return nil` without calling FinishRemove) → the interrupted teardown is never
//     finished → TestFinisherFinishesIsolateRm RED: a `return nil` stub leaves the
//     record present (state.Load != ErrNoRecord); routing to the default returns an
//     error (want nil). One end-to-end test kills both mis-routings.
//   - (alternate — conservative default) make the default arm `return nil` → an op of
//     an unsupported kind is reported finished and its journal discarded unfinished →
//     TestFinisherUnsupportedKindErrors RED (want a non-nil error).

// bootstrap returns a Bootstrap'd layout over a hermetic env, plus a Git and ctx.
func bootstrap(t *testing.T) (*testenv.Env, layout.Layout, *git.Git, context.Context) {
	t.Helper()
	env := testenv.New(t)
	l, err := layout.Resolve(env.Root)
	if err != nil {
		t.Fatalf("layout.Resolve: %v", err)
	}
	if err := l.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return env, l, git.New(gitexec.NewWithEnv("git", env.GitEnv())), context.Background()
}

// seedIsolate bootstraps a hermetic workspace, clones the named SSOTs, and materializes
// an isolate "feat" over them — the realistic precondition for a teardown to finish.
func seedIsolate(t *testing.T, names ...string) (layout.Layout, *git.Git, context.Context) {
	t.Helper()
	env, l, g, ctx := bootstrap(t)
	for _, name := range names {
		origin := env.SeedOrigin(t, name)
		ssot, err := l.Repo(name)
		if err != nil {
			t.Fatalf("layout.Repo(%s): %v", name, err)
		}
		if err := g.EnsureClone(ctx, ssot, "file://"+origin, testenv.DefaultBranch); err != nil {
			t.Fatalf("EnsureClone(%s): %v", name, err)
		}
	}
	specs := make([]isolate.RepoSpec, len(names))
	for i, n := range names {
		specs[i] = isolate.RepoSpec{Name: n, Base: testenv.DefaultBranch}
	}
	if _, err := isolate.New(ctx, l, g, "feat", "op_new", specs); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	return l, g, ctx
}

// TestFinisherFinishesIsolateRm pins the load-bearing route: a rolled-forward isolate_rm
// op handed to the dispatcher's Finisher actually finishes the teardown — the isolate
// (record + worktrees) is gone afterward, and the call returns nil so the executor closes
// the lifecycle. This is the end-to-end proof that the dispatcher routes to the real
// isolate.FinishRemove (not a no-op, not the error default).
func TestFinisherFinishesIsolateRm(t *testing.T) {
	l, g, ctx := seedIsolate(t, "api", "web")

	fin := recovery.Finisher(ctx, l, g)
	op := journal.OpRecovery{OpID: "op_rm_recover", Kind: journal.KindIsolateRm, Task: "feat", Repos: nil}
	if err := fin(op); err != nil {
		t.Fatalf("Finisher(isolate_rm) = %v, want nil (teardown must finish)", err)
	}

	if _, err := state.Load(l.StateDir(), "feat"); !errors.Is(err, state.ErrNoRecord) {
		t.Errorf("state.Load after recovery err = %v, want ErrNoRecord (dispatcher must finish the teardown)", err)
	}
}

// TestFinisherUnsupportedKindErrors pins the conservative default: an op of a kind no
// finisher in this build can complete (e.g. land) returns an error, so the executor
// LEAVES its journal in place and surfaces it — recovery never silently discards an op
// it cannot finish.
func TestFinisherUnsupportedKindErrors(t *testing.T) {
	_, l, g, ctx := bootstrap(t) // no isolate needed; the land op never touches one

	fin := recovery.Finisher(ctx, l, g)
	op := journal.OpRecovery{OpID: "op_land", Kind: journal.KindLand, Task: "feat"}
	if err := fin(op); err == nil {
		t.Errorf("Finisher(land) = nil, want a non-nil error (no finisher for this kind → journal left for retry)")
	}
}
