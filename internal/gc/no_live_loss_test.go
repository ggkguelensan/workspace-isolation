package gc_test

import (
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/gc"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Guard HEAL-GC-NO-LIVE-LOSS — the negative battery that pins gc can never destroy
// recoverable work or resurrect a torn-down isolate (IMPLEMENTATION_PLAN M4, DESIGN
// §7.1). The four-class GC-COLLECT fitness already pins the common preservation cases
// (a dirty or file-committing worktree, a markerless orphan, a live cell). This file
// covers the two SHARPER guarantees the loose §7.1 phrasing implies, each over a real
// git workspace driven through the real Collect executor:
//
//   (i)  "committed-but-equal-to-base work" — a commit whose TREE is byte-identical to
//        the base (an --allow-empty commit, or an add-then-revert) still advances HEAD
//        past the marker, so it is unmerged work gc must refuse. This pins that §7.1
//        "ahead of base" is COMMIT IDENTITY (HEAD != markerSHA), NOT tree-equality: an
//        impl that "optimized" by comparing trees and deciding "nothing changed, safe to
//        reclaim" would silently destroy a real commit.
//   (ii) "no resurrection of a completed-then-deleted isolate" — once a task's registry
//        record is gone (the task completed and was torn down), gc reclaiming its
//        leftover worktree is purely SUBTRACTIVE: it writes no record, re-adds no
//        worktree, and a second sweep is a clean noop. gc tidies leftovers; it never
//        brings a finished task back to life.
//
// DEFERRED (recorded in PROGRESS.md): the PLAN's third case — the HEAL-4-reset + HEAL-6-gc
// composition "cannot prune a discarded sha" — waits on HEAL-4's durable op journal, which
// is what would let gc distinguish a reflog-only commit from a deliberately-discarded one.
// Under the v0 convention (the marker IS the base evidence) a worktree reset back to
// HEAD==marker is clean and not-ahead, hence reclaimable; durable reflog/discard protection
// is HEAL-4's job, not this unit's.

// TestGCRefusesEqualToBaseCommit pins case (i): an isolate cell carrying an EMPTY commit
// (tree identical to base, but HEAD moved to a fresh sha) is blocked_work, never
// reclaimed — and the commit survives the sweep byte-for-byte. The cell is otherwise a
// textbook reclaimable leftover (wi-owned, clean working tree, de-registered), so the
// ONLY thing standing between it and reclamation is the commit-identity "ahead of base"
// signal this test guards.
//
// Non-vacuity (registered): (primary) drop the `|| c.AheadOfBase` term from gc.Classify's
// blocked-work gate so an ahead-but-clean cell falls through to ClassReclaimable → the
// empty commit is destroyed → RED. (alternate, surgical) compute AheadOfBase by comparing
// HEAD^{tree} to markerSHA^{tree} instead of the commit shas → the equal-tree commit reads
// "not ahead" → reclaimed → RED, while the file-committing GC-COLLECT cell (different tree)
// stays GREEN — isolating exactly the commit-identity-vs-tree-equality distinction.
func TestGCRefusesEqualToBaseCommit(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "web")
	if _, err := isolate.New(ctx, l, g, "gone", "op_gone", specs("web")); err != nil {
		t.Fatalf("isolate.New(gone): %v", err)
	}
	// De-register: gone/web is now a not-live, wi-owned, clean leftover — reclaimable
	// EXCEPT for the equal-to-base commit we are about to add.
	if err := state.Delete(l.StateDir(), "gone"); err != nil {
		t.Fatalf("state.Delete(gone): %v", err)
	}

	webWT, _ := l.Isolate("gone", "web")
	ssot, _ := l.Repo("web")
	markerSHA, hasMarker, err := g.OwnedRefSHA(ctx, ssot, "gone", "web")
	if err != nil || !hasMarker {
		t.Fatalf("OwnedRefSHA(gone/web) = (%q, %v, %v), want a present marker", markerSHA, hasMarker, err)
	}

	// An EMPTY commit: tree identical to base, but HEAD advances to a brand-new sha.
	env.Git(t, webWT, "commit", "--allow-empty", "-m", "equal-to-base work")
	committedSHA, err := g.ResolveRef(ctx, webWT, "HEAD")
	if err != nil {
		t.Fatalf("ResolveRef(HEAD): %v", err)
	}
	if committedSHA == markerSHA {
		t.Fatalf("empty commit did not advance HEAD past the marker (HEAD==marker==%s); test would be vacuous", markerSHA)
	}

	res, err := gc.Collect(ctx, l, g, "op_gc_equalbase")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Status != gc.CollectBlocked {
		t.Fatalf("Status = %q, want %q (an equal-to-base commit is unmerged work)", res.Status, gc.CollectBlocked)
	}

	// The cell is surfaced as a hard block, not reclaimed.
	var oc gc.CollectOutcome
	for _, o := range res.Repos {
		if o.Task == "gone" && o.Repo == "web" {
			oc = o
		}
	}
	if oc.Reclaimed || oc.Class != gc.ClassBlockedWork || oc.Reason == "" {
		t.Errorf("gone/web outcome = %+v, want un-reclaimed blocked_work with a reason", oc)
	}

	// Physical effect: nothing destroyed. The worktree, the marker, and — the heart of
	// the guarantee — the commit itself all survive.
	if !pathExists(t, webWT) {
		t.Errorf("gone/web worktree carrying an equal-to-base commit must be preserved, was removed")
	}
	if !markerExists(t, ctx, g, l, "gone", "web") {
		t.Errorf("gone/web marker must be preserved (refs/wi/owned protected), was deleted")
	}
	if head, err := g.ResolveRef(ctx, webWT, "HEAD"); err != nil || head != committedSHA {
		t.Errorf("committed sha must survive the sweep: HEAD = (%q, %v), want %q", head, err, committedSHA)
	}
}

// TestGCNoResurrectionOfCompletedIsolate pins case (ii): gc is purely subtractive. After
// a task's record is deleted (the isolate completed and was torn down), gc reclaiming its
// lone clean leftover removes the worktree+marker and stops there — it writes NO registry
// record, leaves the task dir gone, and a second sweep finds nothing to do. A finished
// task never comes back to life through gc.
//
// Non-vacuity (registered): insert a state.Store of a reconstructed record after the
// reclaim loop in gc.collectTask (a plausible "tombstone what we collected" regression) →
// the post-sweep `state.Load(gone)` no longer returns ErrNoRecord → RED. This mutant
// reddens ONLY this test (no other fitness inspects the registry after Collect), pinning
// the subtractive-only guarantee precisely.
func TestGCNoResurrectionOfCompletedIsolate(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "web")
	if _, err := isolate.New(ctx, l, g, "gone", "op_gone", specs("web")); err != nil {
		t.Fatalf("isolate.New(gone): %v", err)
	}
	// Tear the task down: a clean, wi-owned, de-registered leftover — the lone reclaimable.
	if err := state.Delete(l.StateDir(), "gone"); err != nil {
		t.Fatalf("state.Delete(gone): %v", err)
	}

	res, err := gc.Collect(ctx, l, g, "op_gc_noresurrect")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Status != gc.CollectComplete {
		t.Fatalf("Status = %q, want %q (the lone leftover is cleanly reclaimable)", res.Status, gc.CollectComplete)
	}

	// Subtractive: the worktree is gone and gc wrote no record to resurrect the task.
	webWT, _ := l.Isolate("gone", "web")
	if pathExists(t, webWT) {
		t.Errorf("gone/web worktree must be reclaimed, still present")
	}
	if _, err := state.Load(l.StateDir(), "gone"); !errors.Is(err, state.ErrNoRecord) {
		t.Errorf("gc must write NO record for a reclaimed task: state.Load(gone) err = %v, want ErrNoRecord", err)
	}

	// No resurrection: a second sweep is a clean noop — gc never re-adds a worktree or
	// re-registers the finished task.
	res2, err := gc.Collect(ctx, l, g, "op_gc_noresurrect2")
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if res2.Status != gc.CollectComplete || len(res2.Repos) != 0 {
		t.Errorf("second sweep = {status %q, %d outcomes}, want a clean noop {complete, 0}", res2.Status, len(res2.Repos))
	}
	if pathExists(t, webWT) {
		t.Errorf("gone/web worktree must stay gone after a second sweep (no resurrection)")
	}
}
