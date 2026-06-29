package mirror_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/mirror"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// pushChild clones origin into a throwaway workdir, commits one child of the
// current base tip, and pushes it back — advancing origin's base by exactly one
// commit. It returns the pushed commit's SHA.
func pushChild(t *testing.T, env *testenv.Env, origin string) string {
	t.Helper()
	work := filepath.Join(env.Root, "pusher")
	env.Git(t, env.Root, "clone", origin, work)
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("c1"), 0o644); err != nil {
		t.Fatalf("write feature.txt: %v", err)
	}
	env.Git(t, work, "add", "feature.txt")
	env.Git(t, work, "commit", "-m", "c1")
	c1 := env.Git(t, work, "rev-parse", "HEAD")
	env.Git(t, work, "push", "origin", testenv.DefaultBranch)
	return c1
}

// Guard MIRROR-FETCH — the one network step of the freshness layer (DESIGN §5, §2 #3).
//
// Refresh fetches origin into a repo's SSOT mirror, recomputes how far the local
// base trails origin from LOCAL refs (no second dial), persists a fresh Snapshot,
// and returns it. It is the ONLY part of mirror that touches git/network, so it
// takes a *git.Git and a clock.Clock; the read path takes neither. Critically it
// must NOT advance the base ref — only the remote-tracking ref moves (via
// git.Fetch), so origin_base_sha sees the new tip while local_base_sha stays at
// the old base and the SSOT working tree stays pristine. After fetching an origin
// that advanced by one commit, behind == 1 ⇒ Freshness().Stale, and the snapshot
// it returns is exactly what Load reads back.
//
// Non-vacuity: make Refresh skip the g.Fetch call (classify against the stale
// remote-tracking ref) → behind stays 0, origin_base == local_base, not stale →
// TestRefreshFetchesAndClassifies RED.

func TestRefreshFetchesAndClassifies(t *testing.T) {
	env := testenv.New(t)
	origin := env.SeedOrigin(t, "acme")
	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	dir := filepath.Join(env.Root, "ssot")
	if err := g.EnsureClone(ctx, dir, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	c0, err := g.ResolveRef(ctx, dir, "refs/heads/"+testenv.DefaultBranch)
	if err != nil {
		t.Fatalf("ResolveRef base before: %v", err)
	}

	// Origin advances by one commit AFTER the mirror was cloned, so the mirror is
	// now one behind until it fetches.
	c1 := pushChild(t, env, origin)
	if c0 == c1 {
		t.Fatalf("precondition: pushed child %s equals base tip %s", c1, c0)
	}

	mirrorsDir := filepath.Join(env.Root, "mirrors")
	if err := os.MkdirAll(mirrorsDir, 0o755); err != nil {
		t.Fatalf("mkdir mirrorsDir: %v", err)
	}
	clk := clock.NewFake(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC), 1)

	snap, err := mirror.Refresh(ctx, g, clk, mirrorsDir, "acme", dir, testenv.DefaultBranch)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// The fetch observed the local base one behind origin's advanced tip...
	if snap.BehindOriginAsOfFetch != 1 {
		t.Errorf("BehindOriginAsOfFetch = %d, want 1", snap.BehindOriginAsOfFetch)
	}
	if !snap.Freshness().Stale {
		t.Errorf("Freshness().Stale = false, want true (one commit behind origin)")
	}
	// ...origin_base sees the new tip, local_base stays at the old base (Refresh
	// must not advance the base ref — that is FastForwardBaseRef's job)...
	if snap.OriginBaseSHA != c1 {
		t.Errorf("OriginBaseSHA = %s, want fetched tip %s", snap.OriginBaseSHA, c1)
	}
	if snap.LocalBaseSHA != c0 {
		t.Errorf("LocalBaseSHA = %s, want unmoved base %s", snap.LocalBaseSHA, c0)
	}
	// ...the clock seam stamped fetched_at...
	if snap.FetchedAt != "2026-06-30T12:00:00Z" {
		t.Errorf("FetchedAt = %q, want the injected clock instant", snap.FetchedAt)
	}
	// ...the SSOT mirror stays pristine...
	clean, err := g.IsClean(ctx, dir)
	if err != nil {
		t.Fatalf("IsClean: %v", err)
	}
	if !clean {
		t.Errorf("SSOT mirror dirty after Refresh; it must stay pristine")
	}
	// ...and the returned snapshot is exactly what was persisted.
	loaded, err := mirror.Load(mirrorsDir, "acme")
	if err != nil {
		t.Fatalf("Load after Refresh: %v", err)
	}
	if loaded != snap {
		t.Errorf("persisted snapshot mismatch:\n got = %+v\nwant = %+v", loaded, snap)
	}
}
