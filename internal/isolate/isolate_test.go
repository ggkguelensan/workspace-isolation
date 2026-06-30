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

// Guard ISOLATE-STAMP (M4): a successful isolate new must record the operation's
// holder identity into the isolate-state lock it takes, so the lock self-heal layer
// can later read WHO created the isolate and judge a stale lock's liveness (DESIGN
// §6 / §7.3). The lock file persists after release (Unlock does not unlink), so the
// stamped holder is readable by key once New returns. This is the first of the four
// acquire sites to be wired to (*lock.Held).Stamp.
//
// Non-vacuity mutant (registered): drop the held.Stamp(opID) call in isolate.New →
// the isolate-state lock is acquired but never stamped → its body stays empty →
// lock.ReadHolder returns an "empty holder body" error → this test RED.
func TestNewStampsHolderOnIsolateLock(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api")

	const opID = "op_test_stamp_iso"
	if _, err := isolate.New(ctx, l, g, "feat", opID, specs("api")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	key, err := lock.IsolateState("feat")
	if err != nil {
		t.Fatalf("lock.IsolateState: %v", err)
	}
	h, err := lock.ReadHolder(l.LocksDir(), key)
	if err != nil {
		t.Fatalf("ReadHolder(isolate-state lock): %v — New did not stamp the holder", err)
	}
	if h.OpID != opID {
		t.Errorf("stamped holder OpID = %q, want %q", h.OpID, opID)
	}
	if h.PID != os.Getpid() {
		t.Errorf("stamped holder PID = %d, want this process %d", h.PID, os.Getpid())
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

// outcomeFor returns the RemoveOutcome for repo, failing the test if absent.
func outcomeFor(t *testing.T, res isolate.RemoveResult, repo string) isolate.RemoveOutcome {
	t.Helper()
	for _, oc := range res.Repos {
		if oc.Repo == repo {
			return oc
		}
	}
	t.Fatalf("no RemoveOutcome for %q in %+v", repo, res.Repos)
	return isolate.RemoveOutcome{}
}

// Guard ISOLATE-REMOVE (evidence-positive reclamation, DESIGN §7.1): Remove reclaims
// a repo ONLY when wi can prove it owns it AND it is clean AND not ahead of base. A
// clean, unmoved worktree is reclaimed (worktree + marker gone, dropped from the
// registry); a worktree whose HEAD moved past the creation marker is "ahead of base"
// → a HARD BLOCK surfaced as an unexplained orphan, left intact on disk, in the
// marker store, and in the registry. A repo not in the record is a per-repo error.
//
// Non-vacuity mutant (registered): dropping the `head != marker` gate in reclaimRepo
// lets the ahead-of-base "web" pass straight to RemoveWorktree — its tree is clean,
// so git removes it — wrongly reclaiming local work. That reddens every "web intact"
// assertion below (outcome, on-disk worktree, marker, retained registry entry).
func TestRemoveReclaimsCleanBlocksAheadOfBase(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api")
	cloneSSOT(t, env, l, g, ctx, "web")

	if _, err := isolate.New(ctx, l, g, "feat", "op_test_dddd", specs("api", "web")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	// Move "web" ahead of base with a local commit: tree stays CLEAN, HEAD moves
	// past the creation marker — exactly the "ahead of base" orphan.
	webWT, _ := l.Isolate("feat", "web")
	env.Git(t, webWT, "commit", "--allow-empty", "-m", "local work")

	res, err := isolate.Remove(ctx, l, g, "feat", []string{"api", "web", "ghost"})
	if err != nil {
		t.Fatalf("isolate.Remove: %v", err)
	}
	if res.Status != isolate.RemoveBlocked {
		t.Fatalf("Status = %q, want %q (web is ahead of base)", res.Status, isolate.RemoveBlocked)
	}

	// api: clean and unmoved → reclaimed; worktree dir and marker both gone.
	api := outcomeFor(t, res, "api")
	if !api.Removed || api.Err != nil || api.Reason != "" {
		t.Errorf("api outcome = %+v, want removed/no-err/no-reason", api)
	}
	apiWT, _ := l.Isolate("feat", "api")
	if _, err := os.Stat(apiWT); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("api worktree still present after reclaim (stat err = %v)", err)
	}
	apiSSOT, _ := l.Repo("api")
	if _, exists, _ := g.OwnedRefSHA(ctx, apiSSOT, "feat", "api"); exists {
		t.Errorf("api marker ref survived reclaim, want cleared")
	}

	// web: ahead of base → HARD BLOCK; NOT removed, intact on disk, marker intact.
	web := outcomeFor(t, res, "web")
	if web.Removed || web.Err != nil || web.Reason == "" {
		t.Errorf("web outcome = %+v, want blocked (reason set, not removed, no err)", web)
	}
	if _, err := os.Stat(webWT); err != nil {
		t.Errorf("web worktree missing after a HARD BLOCK (must never be auto-pruned): %v", err)
	}
	webSSOT, _ := l.Repo("web")
	if _, exists, _ := g.OwnedRefSHA(ctx, webSSOT, "feat", "web"); !exists {
		t.Errorf("web marker ref cleared on a HARD BLOCK, want preserved")
	}

	// ghost: never materialized → per-repo error, not a block.
	ghost := outcomeFor(t, res, "ghost")
	if ghost.Removed || !errors.Is(ghost.Err, isolate.ErrRepoNotInIsolate) {
		t.Errorf("ghost outcome = %+v, want ErrRepoNotInIsolate", ghost)
	}

	// Registry: api dropped (reclaimed), web retained (blocked), record still exists.
	rec, err := state.Load(l.StateDir(), "feat")
	if err != nil {
		t.Fatalf("state.Load after partial reclaim: %v", err)
	}
	if len(rec.Repos) != 1 || rec.Repos[0].Repo != "web" {
		t.Errorf("registry repos = %+v, want exactly [web]", rec.Repos)
	}
}

// Guard ISOLATE-REMOVE-TEARDOWN: when Remove reclaims the LAST repo of an isolate,
// the registry record is deleted (the isolate no longer exists) so a later isolate
// rm reports ErrNoRecord, and the emptied task dir is removed. An empty target set
// means "every recorded repo".
//
// Non-vacuity mutant (registered): replacing state.Delete with state.Store (keeping
// an empty-repos husk) in the len==0 branch leaves a record behind, reddening the
// state.Load == ErrNoRecord assertion below.
func TestRemoveAllCleanDeletesRecord(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api")
	cloneSSOT(t, env, l, g, ctx, "web")

	if _, err := isolate.New(ctx, l, g, "feat", "op_test_eeee", specs("api", "web")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	res, err := isolate.Remove(ctx, l, g, "feat", nil) // nil → all recorded repos
	if err != nil {
		t.Fatalf("isolate.Remove: %v", err)
	}
	if res.Status != isolate.RemoveComplete {
		t.Fatalf("Status = %q, want %q", res.Status, isolate.RemoveComplete)
	}
	for _, n := range []string{"api", "web"} {
		oc := outcomeFor(t, res, n)
		if !oc.Removed || oc.Err != nil {
			t.Errorf("%s outcome = %+v, want removed/no-err", n, oc)
		}
		wt, _ := l.Isolate("feat", n)
		if _, err := os.Stat(wt); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s worktree still present after full reclaim (stat err = %v)", n, err)
		}
	}

	// The isolate no longer exists: its record is gone, and the task dir with it.
	if _, err := state.Load(l.StateDir(), "feat"); !errors.Is(err, state.ErrNoRecord) {
		t.Errorf("state.Load after full teardown: err = %v, want ErrNoRecord", err)
	}
	taskDir, _ := l.TaskDir("feat")
	if _, err := os.Stat(taskDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("task dir still present after full teardown (stat err = %v)", err)
	}
}

// Guard ISOLATE-REMOVE (lock): Remove runs under the isolate-state:<task> lock, so a
// concurrent holder makes it refuse with *lock.HeldError (→ exit 6) rather than
// racing a reclamation against another op on the same task (DESIGN §6.1).
func TestRemoveRefusesWhenIsolateStateHeld(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api")
	if _, err := isolate.New(ctx, l, g, "feat", "op_test_ffff", specs("api")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	key, err := lock.IsolateState("feat")
	if err != nil {
		t.Fatalf("lock.IsolateState: %v", err)
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		t.Fatalf("pre-acquire isolate-state lock: %v", err)
	}
	defer func() { _ = held.Release() }()

	_, err = isolate.Remove(ctx, l, g, "feat", nil)
	var he *lock.HeldError
	if !errors.As(err, &he) {
		t.Fatalf("Remove under a held isolate-state lock: err = %v (%T), want *lock.HeldError", err, err)
	}
}

// Guard ISOLATE-REMOVE (missing record): Remove on a task that was never created
// returns state.ErrNoRecord so the CLI can map it to not_found.
func TestRemoveMissingRecordIsErrNoRecord(t *testing.T) {
	_, l, g, ctx := setup(t)
	_, err := isolate.Remove(ctx, l, g, "ghost-task", nil)
	if !errors.Is(err, state.ErrNoRecord) {
		t.Fatalf("Remove on a never-created task: err = %v, want state.ErrNoRecord", err)
	}
}
