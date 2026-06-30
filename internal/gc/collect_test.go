package gc_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/gc"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// pathExists reports whether a worktree directory is still on disk.
func pathExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return err == nil
}

func markerExists(t *testing.T, ctx context.Context, g *git.Git, l layout.Layout, task, repo string) bool {
	t.Helper()
	ssot, err := l.Repo(repo)
	if err != nil {
		t.Fatalf("layout.Repo(%s): %v", repo, err)
	}
	_, exists, err := g.OwnedRefSHA(ctx, ssot, task, repo)
	if err != nil {
		t.Fatalf("OwnedRefSHA(%s/%s): %v", task, repo, err)
	}
	return exists
}

// buildFourClassWorkspace materializes the same four-class workspace the Inspect
// fitness uses: a live isolate "active" (api), and a de-registered "gone" isolate
// whose worktrees linger as not-live leftovers — one reclaimable (web), two blocked
// (db dirty, ledger ahead), one orphan (auth, marker deleted).
func buildFourClassWorkspace(t *testing.T, env *testenv.Env, l layout.Layout, g *git.Git, ctx context.Context) {
	t.Helper()
	gone := []string{"web", "db", "ledger", "auth"}
	for _, n := range append(gone, "api") {
		cloneSSOT(t, env, l, g, ctx, n)
	}
	if _, err := isolate.New(ctx, l, g, "active", "op_active", specs("api")); err != nil {
		t.Fatalf("isolate.New(active): %v", err)
	}
	if _, err := isolate.New(ctx, l, g, "gone", "op_gone", specs(gone...)); err != nil {
		t.Fatalf("isolate.New(gone): %v", err)
	}
	if err := state.Delete(l.StateDir(), "gone"); err != nil {
		t.Fatalf("state.Delete(gone): %v", err)
	}
	dbWT, _ := l.Isolate("gone", "db")
	if err := os.WriteFile(filepath.Join(dbWT, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatalf("dirty db worktree: %v", err)
	}
	ledgerWT, _ := l.Isolate("gone", "ledger")
	if err := os.WriteFile(filepath.Join(ledgerWT, "feature.txt"), []byte("done"), 0o644); err != nil {
		t.Fatalf("write ledger file: %v", err)
	}
	env.Git(t, ledgerWT, "add", "feature.txt")
	env.Git(t, ledgerWT, "commit", "-m", "local work")
	authSSOT, _ := l.Repo("auth")
	if err := g.DeleteOwnedRef(ctx, authSSOT, "gone", "auth"); err != nil {
		t.Fatalf("DeleteOwnedRef(auth): %v", err)
	}
}

// Guard GC-COLLECT — the executor half of `wi gc` (HEAL-2, DESIGN §7.1). Collect is the
// ACTION the read-only Inspect/Classify verdict authorizes: under each task's
// isolate-state lock it RE-observes every on-disk worktree (never trusting a stale
// verdict across the lock) and reclaims ONLY ClassReclaimable cells — RemoveWorktree
// (no --force) + DeleteOwnedRef (clear the spent marker). Every other class is a HARD
// BLOCK left fully intact: a blocked_work worktree (dirty or ahead) is preserved so its
// work is never destroyed (HEAL-GC-NO-LIVE-LOSS), an orphan_unexplained worktree is
// preserved because wi cannot prove it owns it (§7.1), and a still-live cell is not gc's
// to touch at all. Best-effort-all and per-task: one task's block never aborts the rest.
//
// This end-to-end fitness drives the four-class workspace through a real Collect and
// asserts BOTH the result projection AND the physical effect — the only cell whose
// worktree+marker disappear is the reclaimable one; every blocked/orphan/live worktree
// and every protected marker survives byte-for-byte.
//
// Non-vacuity (registered): (primary — no-live-loss) widen the reclaim arm to also act
// on ClassBlockedWork → the dirty/ahead worktrees get removed → the "db/ledger still
// present" assertions RED. (alternate — evidence-positive) widen it to act on
// ClassOrphanUnexplained → the markerless orphan worktree gets removed → the "auth still
// present" assertion RED.
func TestCollectReclaimsOnlyReclaimable(t *testing.T) {
	env, l, g, ctx := setup(t)
	buildFourClassWorkspace(t, env, l, g, ctx)

	res, err := gc.Collect(ctx, l, g, "op_gc_aaaa")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Status != gc.CollectBlocked {
		t.Fatalf("Status = %q, want %q (blocked/orphan cells present)", res.Status, gc.CollectBlocked)
	}

	// Physical effect: ONLY the reclaimable cell's worktree + marker are gone.
	webWT, _ := l.Isolate("gone", "web")
	if pathExists(t, webWT) {
		t.Errorf("gone/web worktree must be reclaimed (removed), still present")
	}
	if markerExists(t, ctx, g, l, "gone", "web") {
		t.Errorf("gone/web owned marker must be deleted on reclaim, still present")
	}
	// Every blocked/orphan/live worktree survives, and protected markers survive.
	for _, cell := range []struct{ task, repo string }{
		{"gone", "db"},     // blocked: dirty
		{"gone", "ledger"}, // blocked: ahead
		{"gone", "auth"},   // orphan: no marker
		{"active", "api"},  // live
	} {
		wt, _ := l.Isolate(cell.task, cell.repo)
		if !pathExists(t, wt) {
			t.Errorf("%s/%s worktree must be left intact, was removed", cell.task, cell.repo)
		}
	}
	if !markerExists(t, ctx, g, l, "gone", "db") || !markerExists(t, ctx, g, l, "gone", "ledger") {
		t.Errorf("blocked-work markers must be preserved (refs/wi/owned protected)")
	}
	if !markerExists(t, ctx, g, l, "active", "api") {
		t.Errorf("live cell marker must be preserved")
	}

	// Result projection: web reclaimed; db/ledger/auth surfaced as hard blocks; the live
	// api cell is not gc's concern and must not appear as an outcome at all.
	byCell := map[string]gc.CollectOutcome{}
	for _, oc := range res.Repos {
		byCell[oc.Task+"/"+oc.Repo] = oc
	}
	if oc := byCell["gone/web"]; !oc.Reclaimed || oc.Class != gc.ClassReclaimable || oc.Err != nil {
		t.Errorf("gone/web outcome = %+v, want reclaimed reclaimable, no err", oc)
	}
	for _, cell := range []struct {
		key  string
		want gc.Class
	}{
		{"gone/db", gc.ClassBlockedWork},
		{"gone/ledger", gc.ClassBlockedWork},
		{"gone/auth", gc.ClassOrphanUnexplained},
	} {
		oc, ok := byCell[cell.key]
		if !ok || oc.Reclaimed || oc.Class != cell.want || oc.Reason == "" {
			t.Errorf("%s outcome = %+v (present=%v), want un-reclaimed %q with a reason", cell.key, oc, ok, cell.want)
		}
	}
	if _, ok := byCell["active/api"]; ok {
		t.Errorf("live cell active/api must not appear in gc outcomes (not gc's to touch)")
	}
}

// TestCollectSkipsBusyTask pins that gc never fights a live isolate op: a task whose
// isolate-state lock is currently HELD (an isolate new/rm/repair in flight) is SKIPPED,
// its worktrees left untouched, and the run reports blocked — rather than stealing the
// lock or destroying a worktree out from under the running op. This is the workspace-gc
// counterpart of the §7.1 "not journaled as live" rule: a held lock IS live activity.
//
// Decision recorded (PROGRESS): gc.Collect diverges from the single-task Remove/Repair
// verbs (which return a held-lock as their own exit-6 contention) — a workspace sweep
// skips the busy task and continues, so one in-flight op never blocks reclaiming
// unrelated leftovers.
func TestCollectSkipsBusyTask(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "web")
	if _, err := isolate.New(ctx, l, g, "gone", "op_gone", specs("web")); err != nil {
		t.Fatalf("isolate.New(gone): %v", err)
	}
	// De-register so gone/web is a reclaimable leftover — Collect WOULD reclaim it...
	if err := state.Delete(l.StateDir(), "gone"); err != nil {
		t.Fatalf("state.Delete(gone): %v", err)
	}
	// ...but the task's lock is held by a (simulated) in-flight op.
	key, err := lock.IsolateState("gone")
	if err != nil {
		t.Fatalf("lock.IsolateState: %v", err)
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		t.Fatalf("Acquire(gone): %v", err)
	}
	defer func() { _ = held.Release() }()

	res, err := gc.Collect(ctx, l, g, "op_gc_busy")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Status != gc.CollectBlocked {
		t.Errorf("Status = %q, want %q (busy task skipped)", res.Status, gc.CollectBlocked)
	}
	webWT, _ := l.Isolate("gone", "web")
	if !pathExists(t, webWT) {
		t.Errorf("gone/web worktree must be left intact while the task lock is held, was removed")
	}
	if !markerExists(t, ctx, g, l, "gone", "web") {
		t.Errorf("gone/web marker must be left intact while the task lock is held, was deleted")
	}
}
