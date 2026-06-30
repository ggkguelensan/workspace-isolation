package land_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/land"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// land.Run is the per-task land orchestrator: under the isolate-state:<task> lock
// (DESIGN §6.1) it writes an all-pending landstate.TaskLand record (the durable
// statement of intent, mirroring how isolate.New writes an all-pending IsolateRecord),
// then lands each repo in turn via the LandRepo cell, folding each outcome into the
// durable record and persisting after every repo so a crash leaves the record
// reflecting EXACTLY the repos already landed. It is SEQUENTIAL and STOP-AT-FIRST-BLOCK:
// the first repo that refuses (a non-fast-forward parks it PhaseBlocked) or faults halts
// the run with the later repos left PhasePending and untouched — the parked record is
// what `land continue`/`land abort` (HEAL-5) resume from. A blocked repo is NOT a Go
// error (it is a recorded refusal → StatusBlocked); Run's error return is reserved for
// failures that prevent the op running at all (a held lock, an unwritable record).
//
// Guard LAND-RUN. Non-vacuity mutants (registered):
//   - neuter the stop-at-first-block (`if rr.Phase != PhaseLanded` → `if false`) so the
//     run keeps landing past a refusal → TestRunParksAtFirstBlockedRepo RED (the later
//     repo's base moves / it is no longer pending);
//   - skip the per-repo landstate.Store inside the loop (the record stays all-pending)
//     → BOTH tests RED on the durable-record assertion;
//   - drop the landed-tip threading in setCell (persist phase+backup but not LandedSHA)
//     → TestRunLandsAllReposComplete RED on the durable LandedSHA assertion ONLY, while
//     its in-memory rr.LandedSHA assertion stays GREEN (two-sided) — pinning that the
//     landed tip `land abort` rewinds from is DURABLE, not merely in the live Result.

// landSetup returns a Bootstrap'd layout over a hermetic env (so .wi/land exists for the
// landstate writer), plus a Git and ctx — the land mirror of isolate_test's setup.
func landSetup(t *testing.T) (*testenv.Env, layout.Layout, *git.Git, context.Context) {
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

// landCloneSSOT seeds a fresh origin for name and clones it into repos/<name>, the
// precondition isolate.New needs to add a worktree off the base.
func landCloneSSOT(t *testing.T, env *testenv.Env, l layout.Layout, g *git.Git, ctx context.Context, name string) {
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

func isoSpecs(names ...string) []isolate.RepoSpec {
	out := make([]isolate.RepoSpec, len(names))
	for i, n := range names {
		out[i] = isolate.RepoSpec{Name: n, Base: testenv.DefaultBranch}
	}
	return out
}

func landSpecs(names ...string) []land.RepoSpec {
	out := make([]land.RepoSpec, len(names))
	for i, n := range names {
		out[i] = land.RepoSpec{Name: n, Base: testenv.DefaultBranch}
	}
	return out
}

func findRepo(t *testing.T, res land.Result, repo string) land.RepoResult {
	t.Helper()
	for _, r := range res.Repos {
		if r.Repo == repo {
			return r
		}
	}
	t.Fatalf("repo %q absent from result", repo)
	return land.RepoResult{}
}

func findCell(t *testing.T, rec landstate.TaskLand, repo string) landstate.RepoLand {
	t.Helper()
	for _, c := range rec.Repos {
		if c.Repo == repo {
			return c
		}
	}
	t.Fatalf("repo %q absent from landstate record", repo)
	return landstate.RepoLand{}
}

func TestRunLandsAllReposComplete(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	names := []string{"api", "web"}
	for _, n := range names {
		landCloneSSOT(t, env, l, g, ctx, n)
	}
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_iso", isoSpecs(names...)); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	// Capture each repo's base tip, then advance its isolate worktree past it.
	baseTip := map[string]string{}
	workTip := map[string]string{}
	for _, n := range names {
		ssot, _ := l.Repo(n)
		baseTip[n] = env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
		wt, _ := l.Isolate(task, n)
		workTip[n] = commitWork(t, env, wt, "feature.txt", "work in "+n+"\n")
	}

	res, err := land.Run(ctx, l, g, task, "op_land_aaaa", landSpecs(names...))
	if err != nil {
		t.Fatalf("land.Run: %v", err)
	}
	if res.Status != land.StatusLanded {
		t.Errorf("Status = %q, want %q", res.Status, land.StatusLanded)
	}

	for _, n := range names {
		rr := findRepo(t, res, n)
		if rr.Phase != landstate.PhaseLanded {
			t.Errorf("%s Phase = %q, want landed", n, rr.Phase)
		}
		if rr.LandedSHA != workTip[n] {
			t.Errorf("%s LandedSHA = %q, want work tip %q", n, rr.LandedSHA, workTip[n])
		}
		if rr.BackupSHA != baseTip[n] {
			t.Errorf("%s BackupSHA = %q, want old base tip %q", n, rr.BackupSHA, baseTip[n])
		}
		ssot, _ := l.Repo(n)
		if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != workTip[n] {
			t.Errorf("%s base after land = %q, want work tip %q", n, got, workTip[n])
		}
	}

	// The durable record reflects every repo landed with its backup anchored.
	rec, err := landstate.Load(l.LandDir(), task)
	if err != nil {
		t.Fatalf("landstate.Load: %v", err)
	}
	for _, n := range names {
		c := findCell(t, rec, n)
		if c.Phase != landstate.PhaseLanded {
			t.Errorf("durable %s Phase = %q, want landed", n, c.Phase)
		}
		if c.BackupSHA != baseTip[n] {
			t.Errorf("durable %s BackupSHA = %q, want %q", n, c.BackupSHA, baseTip[n])
		}
		// The durable record must persist the landed tip too — the value `land abort`
		// asserts the base is STILL at before rewinding it to BackupSHA (the exact-match
		// guard of git.RestoreBaseRef). Distinct from the in-memory rr.LandedSHA above:
		// it is what survives a crash for HEAL-5 to resume from.
		if c.LandedSHA != workTip[n] {
			t.Errorf("durable %s LandedSHA = %q, want work tip %q", n, c.LandedSHA, workTip[n])
		}
	}
}

func TestRunParksAtFirstBlockedRepo(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	names := []string{"api", "web"}
	for _, n := range names {
		landCloneSSOT(t, env, l, g, ctx, n)
	}
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_iso", isoSpecs(names...)); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	// "api" commits work, but a COMPETING land then moves api's base to a divergent
	// commit (via a second worktree) — api's work tip is no longer a fast-forward, so
	// landing api must REFUSE and park.
	apiWt, _ := l.Isolate(task, "api")
	commitWork(t, env, apiWt, "feature.txt", "api work\n")
	apiSSOT, _ := l.Repo("api")
	other := filepath.Join(env.Root, "other-api")
	if err := g.AddWorktree(ctx, apiSSOT, other, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree(other): %v", err)
	}
	divergent := commitWork(t, env, other, "other.txt", "competing\n")
	env.Git(t, apiSSOT, "update-ref", "refs/heads/"+testenv.DefaultBranch, divergent)

	// "web" commits a perfectly landable work tip — but the run must STOP at api and
	// never reach web.
	webWt, _ := l.Isolate(task, "web")
	commitWork(t, env, webWt, "feature.txt", "web work\n")
	webSSOT, _ := l.Repo("web")
	webBaseBefore := env.Git(t, webSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch)

	res, err := land.Run(ctx, l, g, task, "op_land_bbbb", landSpecs(names...))
	if err != nil {
		t.Fatalf("a parked land must be a recorded refusal, not an error: %v", err)
	}
	if res.Status != land.StatusBlocked {
		t.Errorf("Status = %q, want %q", res.Status, land.StatusBlocked)
	}

	if api := findRepo(t, res, "api"); api.Phase != landstate.PhaseBlocked {
		t.Errorf("api Phase = %q, want blocked", api.Phase)
	}
	// web was NOT attempted: still pending in the result, and its base is UNTOUCHED.
	if web := findRepo(t, res, "web"); web.Phase != landstate.PhasePending {
		t.Errorf("web Phase = %q, want pending (the run must stop at the first block)", web.Phase)
	}
	if got := env.Git(t, webSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != webBaseBefore {
		t.Errorf("web base = %q, want it unchanged at %q (web must not be landed after api blocks)", got, webBaseBefore)
	}

	// The durable record parks api blocked and leaves web pending — exactly what
	// `land continue` resumes from.
	rec, err := landstate.Load(l.LandDir(), task)
	if err != nil {
		t.Fatalf("landstate.Load: %v", err)
	}
	if c := findCell(t, rec, "api"); c.Phase != landstate.PhaseBlocked {
		t.Errorf("durable api Phase = %q, want blocked", c.Phase)
	}
	if c := findCell(t, rec, "web"); c.Phase != landstate.PhasePending {
		t.Errorf("durable web Phase = %q, want pending", c.Phase)
	}
}
