package gc_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/gc"
)

// TestClassifyEvidencePositive pins gc's verdict across the signal combinations that
// matter (HEAL-2, DESIGN §7.1). Reclamation is a conjunction — NOT live AND a marker
// proves wi's provenance AND clean AND not ahead of base — and every way of failing
// that conjunction preserves the worktree, by a class that names WHY:
//
//   - a live cell is gc's to leave alone (ClassLive), whatever else is true;
//   - no marker → ClassOrphanUnexplained, the loud hard block (no provenance);
//   - wi-owned but dirty or ahead → ClassBlockedWork (would destroy live work);
//   - wi-owned, clean, not ahead, not live → ClassReclaimable, the lone collectable.
func TestClassifyEvidencePositive(t *testing.T) {
	cases := []struct {
		name string
		cand gc.Candidate
		want gc.Class
	}{
		// The one reclaimable shape: provenance proven, no work, not live.
		{"owned-clean-behind-dead", gc.Candidate{HasMarker: true, Clean: true}, gc.ClassReclaimable},

		// Live short-circuits every other signal — even a clean, owned, behind cell.
		{"live-but-otherwise-reclaimable", gc.Candidate{HasMarker: true, Clean: true, Live: true}, gc.ClassLive},
		{"live-and-dirty", gc.Candidate{HasMarker: true, Live: true}, gc.ClassLive},
		{"live-without-marker", gc.Candidate{Live: true}, gc.ClassLive},

		// No marker → orphan, regardless of how clean/behind it looks.
		{"no-marker-clean", gc.Candidate{Clean: true}, gc.ClassOrphanUnexplained},
		{"no-marker-dirty", gc.Candidate{}, gc.ClassOrphanUnexplained},
		{"no-marker-ahead", gc.Candidate{AheadOfBase: true}, gc.ClassOrphanUnexplained},

		// Owned but carrying work → blocked, never collected.
		{"owned-dirty", gc.Candidate{HasMarker: true}, gc.ClassBlockedWork},
		{"owned-ahead-but-clean", gc.Candidate{HasMarker: true, Clean: true, AheadOfBase: true}, gc.ClassBlockedWork},
		{"owned-dirty-and-ahead", gc.Candidate{HasMarker: true, AheadOfBase: true}, gc.ClassBlockedWork},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gc.Classify(tc.cand); got != tc.want {
				t.Errorf("Classify(%+v) = %q, want %q", tc.cand, got, tc.want)
			}
		})
	}
}

// TestClassifyNeverReclaimsWithoutMarker isolates the evidence-positive keystone: a
// candidate with NO wi-owned marker can NEVER be reclaimable, no matter how safe it
// otherwise looks (clean, not ahead, not live). Without the marker wi cannot prove it
// created the worktree, so it may be the user's own — collecting it is exactly the
// data-loss path §7.1 forbids.
//
// Registered non-vacuity mutant (GC-CLASSIFY, evidence-positive limb): deleting the
//
//	case !c.HasMarker: return ClassOrphanUnexplained
//
// gate lets a markerless clean candidate fall through to ClassReclaimable, reddening
// this test while the live/blocked/reclaimable rows stay green.
func TestClassifyNeverReclaimsWithoutMarker(t *testing.T) {
	for _, c := range []gc.Candidate{
		{Clean: true}, // clean, behind, dead — but no provenance
		{Clean: true, AheadOfBase: false},
		{}, // dirty too
	} {
		if got := gc.Classify(c); got == gc.ClassReclaimable {
			t.Fatalf("a worktree with no wi-owned marker must never be reclaimable, got %q for %+v", got, c)
		}
		if got := gc.Classify(c); got != gc.ClassOrphanUnexplained {
			t.Fatalf("a markerless, non-live worktree must be orphan_unexplained, got %q for %+v", got, c)
		}
	}
}

// TestClassifyNeverReclaimsLiveWork isolates the no-live-loss keystone
// (HEAL-GC-NO-LIVE-LOSS): a wi-owned worktree that still carries work — uncommitted
// (dirty) OR committed-but-unmerged (ahead of base) — must NEVER be reclaimable.
// Collecting it would destroy work that was never landed.
//
// Registered non-vacuity mutant (GC-CLASSIFY, no-live-loss limb): deleting the
//
//	case !c.Clean || c.AheadOfBase: return ClassBlockedWork
//
// gate lets a dirty/ahead owned worktree fall through to ClassReclaimable, reddening
// this test while the orphan/live/clean-reclaimable rows stay green.
func TestClassifyNeverReclaimsLiveWork(t *testing.T) {
	for _, c := range []gc.Candidate{
		{HasMarker: true}, // owned but dirty
		{HasMarker: true, Clean: true, AheadOfBase: true}, // owned, clean, but ahead
		{HasMarker: true, AheadOfBase: true},              // owned, dirty AND ahead
	} {
		if got := gc.Classify(c); got == gc.ClassReclaimable {
			t.Fatalf("a wi-owned worktree carrying work must never be reclaimable, got %q for %+v", got, c)
		}
		if got := gc.Classify(c); got != gc.ClassBlockedWork {
			t.Fatalf("a wi-owned worktree carrying work must be blocked_work, got %q for %+v", got, c)
		}
	}
}
