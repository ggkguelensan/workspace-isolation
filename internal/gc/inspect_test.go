package gc_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/gc"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
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

func specs(names ...string) []isolate.RepoSpec {
	out := make([]isolate.RepoSpec, len(names))
	for i, n := range names {
		out[i] = isolate.RepoSpec{Name: n, Base: testenv.DefaultBranch}
	}
	return out
}

// Guard GC-INSPECT — the read-only workspace gc sweep (HEAL-2, DESIGN §7.1/§7.5).
// Inspect is the data-gathering half of `wi gc`: it enumerates every on-disk isolate
// worktree (the candidate population — which is what makes the orphan inventory
// structural: a worktree wi cannot prove it owns SURFACES), observes the four §7.1
// signals per cell (HasMarker via the owned ref, Live via the registry, Clean +
// AheadOfBase via the worktree), and runs each through the already-pinned gc.Classify.
// Like isolate.Inspect/lock.List it takes no lock, mutates nothing, dials no network.
//
// This end-to-end fitness drives one worktree into each of the four verdict classes
// over a REAL git workspace, so the whole observe→classify chain is exercised, not a
// hand-built Candidate:
//   - a still-live isolate cell                         → ClassLive (gc leaves it alone)
//   - a wi-owned, clean, not-ahead, de-registered cell  → ClassReclaimable
//   - a wi-owned cell with uncommitted changes          → ClassBlockedWork (dirty)
//   - a wi-owned cell whose HEAD moved past its marker  → ClassBlockedWork (ahead)
//   - a worktree whose owned marker is gone             → ClassOrphanUnexplained
//
// Non-vacuity (registered): (primary) stop populating the live-set → the live cell
// misclassifies as ClassReclaimable → RED, pinning that Inspect threads state.List's
// Live signal into the §7.1 "never collect live work" gate. (alternate) break out of
// the per-task repo enumeration after the first cell → fewer candidates than worktrees
// → the count assertion RED.
func TestInspectClassifiesEachWorktree(t *testing.T) {
	env, l, g, ctx := setup(t)
	names := []string{"web", "db", "ledger", "auth"}
	for _, n := range append(names, "api") {
		cloneSSOT(t, env, l, g, ctx, n)
	}

	// "active" stays a live isolate (its record is kept) → api is ClassLive.
	if _, err := isolate.New(ctx, l, g, "active", "op_active", specs("api")); err != nil {
		t.Fatalf("isolate.New(active): %v", err)
	}
	// "gone" is a completed isolate whose registry record we delete, leaving its
	// worktrees + markers on disk as not-live leftovers — exactly what gc reclaims.
	if _, err := isolate.New(ctx, l, g, "gone", "op_gone", specs(names...)); err != nil {
		t.Fatalf("isolate.New(gone): %v", err)
	}
	if err := state.Delete(l.StateDir(), "gone"); err != nil {
		t.Fatalf("state.Delete(gone): %v", err)
	}

	// gone/db → dirty (uncommitted change in the worktree).
	dbWT, _ := l.Isolate("gone", "db")
	if err := os.WriteFile(filepath.Join(dbWT, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatalf("dirty db worktree: %v", err)
	}
	// gone/ledger → ahead (a committed-but-unmerged commit moves HEAD past the marker).
	ledgerWT, _ := l.Isolate("gone", "ledger")
	if err := os.WriteFile(filepath.Join(ledgerWT, "feature.txt"), []byte("done"), 0o644); err != nil {
		t.Fatalf("write ledger file: %v", err)
	}
	env.Git(t, ledgerWT, "add", "feature.txt")
	env.Git(t, ledgerWT, "commit", "-m", "local work")
	// gone/auth → orphan (delete the owned marker; the worktree stays).
	authSSOT, _ := l.Repo("auth")
	if err := g.DeleteOwnedRef(ctx, authSSOT, "gone", "auth"); err != nil {
		t.Fatalf("DeleteOwnedRef(auth): %v", err)
	}

	cands, err := gc.Inspect(ctx, l, g)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	want := map[string]gc.Class{
		"active/api":  gc.ClassLive,
		"gone/web":    gc.ClassReclaimable,
		"gone/db":     gc.ClassBlockedWork,
		"gone/ledger": gc.ClassBlockedWork,
		"gone/auth":   gc.ClassOrphanUnexplained,
	}
	if len(cands) != len(want) {
		t.Fatalf("Inspect returned %d candidates, want %d (%+v)", len(cands), len(want), cands)
	}
	got := map[string]gc.Class{}
	for _, c := range cands {
		got[c.Task+"/"+c.Repo] = gc.Classify(c)
	}
	for cell, wc := range want {
		if got[cell] != wc {
			t.Errorf("%s classified %q, want %q", cell, got[cell], wc)
		}
	}
}

// TestInspectEmptyWorkspaceIsEmpty pins that a workspace with no isolate worktrees
// (isolas/ absent or empty) is the empty candidate list, not an error — gc over a
// fresh workspace has nothing to reclaim, the same idempotent posture state.List and
// the reclamation verbs take.
func TestInspectEmptyWorkspaceIsEmpty(t *testing.T) {
	_, l, g, ctx := setup(t)
	cands, err := gc.Inspect(ctx, l, g)
	if err != nil || len(cands) != 0 {
		t.Fatalf("Inspect(empty workspace) = (%+v, %v), want (empty, nil)", cands, err)
	}
}
