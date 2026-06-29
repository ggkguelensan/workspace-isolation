package isolate_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// setup returns a Bootstrap'd layout over a hermetic env, plus a Git and ctx.
func setup(t *testing.T) (*testenv.Env, layout.Layout, *git.Git, context.Context) {
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

// cloneSSOT seeds a hermetic origin for name and materializes its SSOT clone at
// repos/<name> (the precondition isolate.New needs to add a worktree off it).
func cloneSSOT(t *testing.T, env *testenv.Env, l layout.Layout, g *git.Git, ctx context.Context, name string) {
	t.Helper()
	origin := env.SeedOrigin(t, name)
	ssot, err := l.Repo(name)
	if err != nil {
		t.Fatalf("layout.Repo(%s): %v", name, err)
	}
	if err := g.EnsureClone(ctx, ssot, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone(%s): %v", name, err)
	}
}

// specs builds RepoSpecs for the given names, all based on DefaultBranch.
func specs(names ...string) []isolate.RepoSpec {
	out := make([]isolate.RepoSpec, len(names))
	for i, n := range names {
		out[i] = isolate.RepoSpec{Name: n, Base: testenv.DefaultBranch}
	}
	return out
}

// Guard ISOLATE-NEW (complete): New materializes one detached worktree per repo
// off the SSOT base, records each as a wi-owned marker, and reports complete.
func TestNewMaterializesAllReposComplete(t *testing.T) {
	env, l, g, ctx := setup(t)
	names := []string{"api", "web", "db"}
	for _, n := range names {
		cloneSSOT(t, env, l, g, ctx, n)
	}

	res, err := isolate.New(ctx, l, g, "feat", "op_test_aaaa", specs(names...))
	if err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	if res.Status != isolate.StatusComplete {
		t.Fatalf("Status = %q, want %q", res.Status, isolate.StatusComplete)
	}

	rec, err := state.Load(l.StateDir(), "feat")
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	for i, n := range names {
		oc := res.Repos[i]
		if oc.Repo != n || oc.Stage != state.StageCreated || oc.Err != nil {
			t.Errorf("repo %s outcome = %+v, want created/no-err", n, oc)
		}
		// The worktree exists and is DETACHED (no branch checked out) — DESIGN §5.
		wt, _ := l.Isolate("feat", n)
		if head := env.Git(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); head != "HEAD" {
			t.Errorf("%s worktree not detached: abbrev-ref HEAD = %q", n, head)
		}
		// Evidence-positive ownership: the marker ref records the worktree's tip.
		ssot, _ := l.Repo(n)
		sha, exists, err := g.OwnedRefSHA(ctx, ssot, "feat", n)
		if err != nil || !exists || sha != oc.SHA {
			t.Errorf("%s marker = (%q,%v,%v), want (%q,true,nil)", n, sha, exists, err, oc.SHA)
		}
		// Durable registry agrees: created.
		if rec.Repos[i].Repo != n || rec.Repos[i].Stage != state.StageCreated {
			t.Errorf("durable record[%d] = %+v, want %s/created", i, rec.Repos[i], n)
		}
		// The SSOT base ref was NOT moved by adding a worktree off it (DESIGN §5).
		if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != oc.SHA {
			t.Errorf("%s SSOT base ref moved: %q, want %q", n, got, oc.SHA)
		}
	}
}

// Guard ISOLATE-NEW (durable partial success — the core, DESIGN §6.3): when a repo
// fails to materialize, New stops on it, KEEPS every repo completed before it, and
// never attempts repos after it — and the durable registry reflects EXACTLY that
// completed set so the op is resumable. Here "web" has no SSOT clone, so its
// worktree add fails; "api" (before it) must stay created, "db" (after it) pending.
//
// Non-vacuity mutant (registered): dropping the stop-on-first-fail `return` in
// New lets the loop continue past the failed "web" and materialize "db" — "db"
// then turns StageCreated in both the result and the durable record, reddening the
// "db pending / not attempted" assertions below.
func TestNewStopsOnFirstFailWithDurablePartialSuccess(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api") // exists
	cloneSSOT(t, env, l, g, ctx, "db")  // exists, but must NOT be reached
	// "web" intentionally has no repos/web SSOT → its worktree add fails.

	res, err := isolate.New(ctx, l, g, "feat", "op_test_bbbb", specs("api", "web", "db"))
	if err != nil {
		t.Fatalf("isolate.New returned a hard error, want a recorded partial success: %v", err)
	}
	if res.Status != isolate.StatusPartial {
		t.Fatalf("Status = %q, want %q", res.Status, isolate.StatusPartial)
	}

	// api completed; web failed; db not attempted.
	if res.Repos[0].Repo != "api" || res.Repos[0].Stage != state.StageCreated || res.Repos[0].Err != nil {
		t.Errorf("api outcome = %+v, want created/no-err", res.Repos[0])
	}
	if res.Repos[1].Repo != "web" || res.Repos[1].Err == nil {
		t.Errorf("web outcome = %+v, want a recorded error", res.Repos[1])
	}
	if res.Repos[2].Repo != "db" || res.Repos[2].Stage != state.StagePending || res.Repos[2].Err != nil {
		t.Errorf("db outcome = %+v, want pending/not-attempted", res.Repos[2])
	}

	// The durable registry reflects EXACTLY the completed set (resumable).
	rec, err := state.Load(l.StateDir(), "feat")
	if err != nil {
		t.Fatalf("state.Load after partial: %v", err)
	}
	want := map[string]state.Stage{"api": state.StageCreated, "web": state.StagePending, "db": state.StagePending}
	for _, rr := range rec.Repos {
		if rr.Stage != want[rr.Repo] {
			t.Errorf("durable %s = %q, want %q", rr.Repo, rr.Stage, want[rr.Repo])
		}
	}

	// On disk: api's worktree is durable (NOT rolled back); db's was never created.
	apiWT, _ := l.Isolate("feat", "api")
	if _, err := os.Stat(apiWT); err != nil {
		t.Errorf("api worktree missing after partial success (rolled back?): %v", err)
	}
	dbWT, _ := l.Isolate("feat", "db")
	if _, err := os.Stat(dbWT); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("db worktree exists (stat err = %v); stop-on-first-fail must not materialize it", err)
	}
	// api's SSOT stays pristine — a completed worktree does not dirty the SSOT.
	apiSSOT, _ := l.Repo("api")
	if clean, err := g.IsClean(ctx, apiSSOT); err != nil || !clean {
		t.Errorf("api SSOT not pristine after partial (clean=%v, err=%v)", clean, err)
	}
}

// Guard ISOLATE-NEW (lock): New runs under the isolate-state:<task> lock, so a
// concurrent holder makes it refuse with *lock.HeldError (→ exit 6) rather than
// racing a second materialization of the same task (DESIGN §6.1).
func TestNewRefusesWhenIsolateStateHeld(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api")

	key, err := lock.IsolateState("feat")
	if err != nil {
		t.Fatalf("lock.IsolateState: %v", err)
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		t.Fatalf("pre-acquire isolate-state lock: %v", err)
	}
	defer func() { _ = held.Release() }()

	_, err = isolate.New(ctx, l, g, "feat", "op_test_cccc", specs("api"))
	var he *lock.HeldError
	if !errors.As(err, &he) {
		t.Fatalf("New under a held isolate-state lock: err = %v (%T), want *lock.HeldError", err, err)
	}
}
