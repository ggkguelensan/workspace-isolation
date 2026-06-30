package sync_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/mirror"
	syncpkg "github.com/ggkguelensan/workspace-isolation/internal/sync"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// harness is the hermetic real-git substrate every SYNC-RUN test runs in: a
// Bootstrap'd layout over a testenv root plus the real git driver wired to the
// hermetic environment, so Run exercises actual clone/fetch/update-ref against
// local file:// origins.
type harness struct {
	env *testenv.Env
	l   layout.Layout
	g   *git.Git
	ctx context.Context
}

func newHarness(t *testing.T) harness {
	t.Helper()
	env := testenv.New(t)
	l, err := layout.Resolve(env.Root)
	if err != nil {
		t.Fatalf("resolve layout: %v", err)
	}
	if err := l.Bootstrap(); err != nil {
		t.Fatalf("bootstrap layout: %v", err)
	}
	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	return harness{env: env, l: l, g: g, ctx: context.Background()}
}

func (h harness) run(t *testing.T, opID string, specs ...syncpkg.RepoSpec) syncpkg.Result {
	t.Helper()
	res, err := syncpkg.Run(h.ctx, h.l, h.g, clock.System{}, opID, specs)
	if err != nil {
		t.Fatalf("Run returned an op-level error: %v", err)
	}
	return res
}

func (h harness) baseRef(t *testing.T, repo string) string {
	t.Helper()
	ssot, err := h.l.Repo(repo)
	if err != nil {
		t.Fatalf("layout.Repo(%q): %v", repo, err)
	}
	sha, err := h.g.ResolveRef(h.ctx, ssot, "refs/heads/main")
	if err != nil {
		t.Fatalf("resolve on-disk base ref for %q: %v", repo, err)
	}
	return sha
}

// advanceOrigin pushes one new commit onto the bare origin's main and returns the
// new tip SHA, so a subsequent sync has something to fast-forward to. It uses raw
// testenv git (unrestricted, local file:// work) — the egress belt only constrains
// wi's own git driver.
func advanceOrigin(t *testing.T, env *testenv.Env, origin string) string {
	t.Helper()
	work := filepath.Join(env.Root, "_advance_"+filepath.Base(origin))
	env.Git(t, env.Root, "clone", origin, work)
	if err := os.WriteFile(filepath.Join(work, "CHANGE.md"), []byte("advance\n"), 0o644); err != nil {
		t.Fatalf("write change file: %v", err)
	}
	env.Git(t, work, "add", "CHANGE.md")
	env.Git(t, work, "commit", "-m", "advance origin")
	env.Git(t, work, "push", "origin", "main")
	return env.Git(t, work, "rev-parse", "HEAD")
}

// TestSyncMaterializesAndAdvancesBase pins the core sync contract on a fresh repo:
// the SSOT is lazily cloned, its base ref is advanced to the origin tip, the working
// tree stays pristine, and a non-stale freshness snapshot is persisted.
func TestSyncMaterializesAndAdvancesBase(t *testing.T) {
	h := newHarness(t)
	origin := h.env.SeedOrigin(t, "api")
	originTip := h.env.Git(t, origin, "rev-parse", "refs/heads/main")

	res := h.run(t, "op_fresh", syncpkg.RepoSpec{Name: "api", URL: "file://" + origin, Base: []string{"main"}})

	if res.Status != syncpkg.StatusComplete {
		t.Fatalf("status = %q, want complete", res.Status)
	}
	if len(res.Repos) != 1 {
		t.Fatalf("got %d repo outcomes, want 1", len(res.Repos))
	}
	oc := res.Repos[0]
	if oc.Err != nil {
		t.Fatalf("repo api failed: %v", oc.Err)
	}
	if oc.Snapshot.LocalBaseSHA != originTip {
		t.Errorf("snapshot local base = %s, want origin tip %s", oc.Snapshot.LocalBaseSHA, originTip)
	}
	if oc.Snapshot.BehindOriginAsOfFetch != 0 {
		t.Errorf("behind = %d, want 0 right after advancing to origin", oc.Snapshot.BehindOriginAsOfFetch)
	}

	// The base ref actually moved on disk, independent of the returned snapshot.
	if got := h.baseRef(t, "api"); got != originTip {
		t.Errorf("on-disk base ref = %s, want origin tip %s", got, originTip)
	}

	// Freshness was persisted and reads back as not-stale.
	loaded, err := mirror.Load(h.l.MirrorsDir(), "api")
	if err != nil {
		t.Fatalf("load persisted snapshot: %v", err)
	}
	if loaded.BehindOriginAsOfFetch != 0 || loaded.Freshness().Stale {
		t.Errorf("persisted freshness = %+v, want behind 0 / not stale", loaded)
	}

	// SSOT invariant (DESIGN §5): the mirror working tree is pristine after a sync.
	ssot, _ := h.l.Repo("api")
	clean, err := h.g.IsClean(h.ctx, ssot)
	if err != nil {
		t.Fatalf("IsClean: %v", err)
	}
	if !clean {
		t.Errorf("SSOT working tree is dirty after sync, want pristine")
	}
}

// TestSyncFastForwardsToNewOriginTip is the load-bearing advance test: after the
// origin moves, a second sync must fast-forward the local base to the new tip. This
// is the assertion the registered mutant (dropping the FastForwardBaseRef call)
// reddens — the on-disk base ref would otherwise stay at the old tip.
func TestSyncFastForwardsToNewOriginTip(t *testing.T) {
	h := newHarness(t)
	origin := h.env.SeedOrigin(t, "api")
	spec := syncpkg.RepoSpec{Name: "api", URL: "file://" + origin, Base: []string{"main"}}

	// First sync materializes the SSOT at the seed tip.
	if oc := h.run(t, "op1", spec).Repos[0]; oc.Err != nil {
		t.Fatalf("first sync failed: %v", oc.Err)
	}

	newTip := advanceOrigin(t, h.env, origin)

	// Second sync must advance the base ref to the new origin tip.
	oc := h.run(t, "op2", spec).Repos[0]
	if oc.Err != nil {
		t.Fatalf("second sync failed: %v", oc.Err)
	}
	if oc.Snapshot.LocalBaseSHA != newTip {
		t.Errorf("snapshot local base = %s, want new origin tip %s", oc.Snapshot.LocalBaseSHA, newTip)
	}
	if got := h.baseRef(t, "api"); got != newTip {
		t.Errorf("on-disk base ref = %s, want new origin tip %s (was the base fast-forwarded?)", got, newTip)
	}
}

// TestSyncContinuesOnFailureAndReportsPartial pins the continue-on-fail contract:
// an unreachable repo listed FIRST fails, yet the reachable repo after it is still
// synced and the overall status is partial.
func TestSyncContinuesOnFailureAndReportsPartial(t *testing.T) {
	h := newHarness(t)
	origin := h.env.SeedOrigin(t, "api")
	originTip := h.env.Git(t, origin, "rev-parse", "refs/heads/main")

	res := h.run(t, "op",
		syncpkg.RepoSpec{Name: "ghost", URL: "file://" + filepath.Join(h.env.Root, "does-not-exist.git"), Base: []string{"main"}},
		syncpkg.RepoSpec{Name: "api", URL: "file://" + origin, Base: []string{"main"}},
	)

	if res.Status != syncpkg.StatusPartial {
		t.Fatalf("status = %q, want partial", res.Status)
	}
	if len(res.Repos) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(res.Repos))
	}
	if ghost := res.Repos[0]; ghost.Repo != "ghost" || ghost.Err == nil {
		t.Errorf("repos[0] = %+v, want ghost with a non-nil Err", ghost)
	}
	// The key assertion: the reachable repo AFTER the failed one was still synced.
	api := res.Repos[1]
	if api.Repo != "api" || api.Err != nil {
		t.Fatalf("repos[1] = %+v, want api synced (continue-on-fail)", api)
	}
	if api.Snapshot.LocalBaseSHA != originTip {
		t.Errorf("api base = %s, want origin tip %s", api.Snapshot.LocalBaseSHA, originTip)
	}
	if got := h.baseRef(t, "api"); got != originTip {
		t.Errorf("api on-disk base = %s, want %s", got, originTip)
	}
}

// seedOriginBranch pushes a new branch onto the bare origin off its main tip, so a
// sync probing an ordered base candidate list can find it on the remote. Local
// file:// work through unrestricted testenv git.
func seedOriginBranch(t *testing.T, env *testenv.Env, origin, branch string) {
	t.Helper()
	work := filepath.Join(env.Root, "_branch_"+branch+"_"+filepath.Base(origin))
	env.Git(t, env.Root, "clone", origin, work)
	env.Git(t, work, "switch", "-c", branch)
	env.Git(t, work, "push", "origin", branch)
}

// Guard SYNC-BASE-CANDIDATES (DESIGN §1): sync is the ONE seam that resolves an
// ordered base candidate list against ORIGIN, because at first sync the single-branch
// mirror does not exist yet. ["dev","main"] must clone whichever earlier candidate
// exists on origin: dev when origin has it, main when it does not. This is what makes
// `defaults.base: ["dev","main"]` mean "prefer dev, else main" end-to-end.
//
// Non-vacuity mutant (registered): in resolveSyncBase, on the no-mirror path return
// firstBase(candidates) instead of consulting FirstExistingRemoteHead → the main-only
// origin would try to clone --branch dev and fail → the only-main repo's outcome goes
// from synced to errored → TestSyncResolvesBaseAgainstOrigin RED.
func TestSyncResolvesBaseAgainstOrigin(t *testing.T) {
	h := newHarness(t)

	// Origin A has both dev and main: ["dev","main"] must clone dev.
	originA := h.env.SeedOrigin(t, "withdev")
	seedOriginBranch(t, h.env, originA, "dev")
	devTip := h.env.Git(t, originA, "rev-parse", "refs/heads/dev")

	// Origin B has only main: ["dev","main"] must fall back to main.
	originB := h.env.SeedOrigin(t, "nodev")
	mainTip := h.env.Git(t, originB, "rev-parse", "refs/heads/main")

	res := h.run(t, "op_cand",
		syncpkg.RepoSpec{Name: "withdev", URL: "file://" + originA, Base: []string{"dev", "main"}},
		syncpkg.RepoSpec{Name: "nodev", URL: "file://" + originB, Base: []string{"dev", "main"}},
	)
	if res.Status != syncpkg.StatusComplete {
		t.Fatalf("status = %q, want complete: %+v", res.Status, res.Repos)
	}

	withdev := res.Repos[0]
	if withdev.Err != nil {
		t.Fatalf("withdev sync failed: %v", withdev.Err)
	}
	if withdev.Base != "dev" {
		t.Errorf("withdev resolved base = %q, want dev (earlier candidate exists on origin)", withdev.Base)
	}
	if withdev.Snapshot.LocalBaseSHA != devTip {
		t.Errorf("withdev base sha = %s, want dev tip %s", withdev.Snapshot.LocalBaseSHA, devTip)
	}

	nodev := res.Repos[1]
	if nodev.Err != nil {
		t.Fatalf("nodev sync failed: %v", nodev.Err)
	}
	if nodev.Base != "main" {
		t.Errorf("nodev resolved base = %q, want main (dev absent on origin)", nodev.Base)
	}
	if nodev.Snapshot.LocalBaseSHA != mainTip {
		t.Errorf("nodev base sha = %s, want main tip %s", nodev.Snapshot.LocalBaseSHA, mainTip)
	}
}

// Guard SYNC-STAMP (M4): syncing a repo holds its repo:<name> lock for the whole
// fetch/ff, and must record the operation's holder identity into that lock so the
// self-heal layer can later read WHO is syncing and judge a stale lock's liveness
// (DESIGN §6 / §7.3). The lock file persists after release (Unlock does not
// unlink), so the stamped holder is readable by key once Run returns. This wires
// the hottest-contention acquire site — parallel agents racing `wi sync` on the
// same repo:<name> key.
//
// Non-vacuity mutant (registered): drop the held.Stamp(opID) call in syncOne (or
// thread opID but never call Stamp) → the repo lock is acquired but never stamped
// → its body stays empty → lock.ReadHolder returns "empty holder body" → RED.
func TestSyncStampsHolderOnRepoLock(t *testing.T) {
	h := newHarness(t)
	origin := h.env.SeedOrigin(t, "api")

	const opID = "op_sync_stamp"
	res := h.run(t, opID, syncpkg.RepoSpec{Name: "api", URL: "file://" + origin, Base: []string{"main"}})
	if res.Status != syncpkg.StatusComplete {
		t.Fatalf("Status = %q, want %q", res.Status, syncpkg.StatusComplete)
	}

	key, err := lock.Repo("api")
	if err != nil {
		t.Fatalf("lock.Repo: %v", err)
	}
	holder, err := lock.ReadHolder(h.l.LocksDir(), key)
	if err != nil {
		t.Fatalf("ReadHolder(repo:api lock): %v — sync did not stamp the holder", err)
	}
	if holder.OpID != opID {
		t.Errorf("stamped holder OpID = %q, want %q", holder.OpID, opID)
	}
	if holder.PID != os.Getpid() {
		t.Errorf("stamped holder PID = %d, want this process %d", holder.PID, os.Getpid())
	}
}
