package isolate_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
)

// TestClassifyEvidencePositive pins the full 2×2 truth table of the drift
// classifier (HEAL-1, DESIGN §7.1/§7.4). The verdict is a pure function of the two
// PHYSICAL ownership signals — does wi's owned marker ref prove wi created the cell,
// and does the worktree exist on disk — because §7.1 makes the MARKER, not the
// registry record, the authority on whether a cell should exist.
//
// The two load-bearing rows are the no-resurrection keystone:
//   - (marker present, worktree absent) → MissingWorktree: safe to re-materialize
//     PRECISELY because the surviving marker proves this is not a completed-then-
//     deleted op.
//   - (marker absent, worktree absent) → Reclaimed: a completed-then-deleted op has
//     had its marker unlinked by isolate rm, so it must NEVER be resurrected.
func TestClassifyEvidencePositive(t *testing.T) {
	cases := []struct {
		name           string
		markerExists   bool
		worktreeExists bool
		want           isolate.Classification
	}{
		{"owned-and-present", true, true, isolate.ClassConsistent},
		{"owned-but-worktree-gone", true, false, isolate.ClassMissingWorktree},
		{"worktree-without-marker", false, true, isolate.ClassOrphanWorktree},
		{"neither-present", false, false, isolate.ClassReclaimed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isolate.Classify(tc.markerExists, tc.worktreeExists)
			if got != tc.want {
				t.Errorf("Classify(marker=%v, worktree=%v) = %q, want %q",
					tc.markerExists, tc.worktreeExists, got, tc.want)
			}
		})
	}
}

// TestClassifyNoResurrection isolates the single most dangerous direction: a cell
// with NO surviving marker and NO worktree must classify as Reclaimed, never as a
// re-materialize candidate. A classifier that keys re-materialization off the
// worktree's absence alone (ignoring the marker) would resurrect a deliberately
// removed isolate — the exact data-creation path DESIGN §7.4 HEAL-1 forbids. This is
// the registered non-vacuity guard: the mutant
//
//	case !worktreeExists: return ClassMissingWorktree   // BUG: ignores the marker
//
// reddens here (neither-present reads MissingWorktree, want Reclaimed) while every
// other row stays green, proving the test pins the marker as the re-materialize
// authority.
func TestClassifyNoResurrection(t *testing.T) {
	if got := isolate.Classify(false, false); got != isolate.ClassReclaimed {
		t.Fatalf("a cell with no marker and no worktree must be Reclaimed (no resurrection), got %q", got)
	}
	if got := isolate.Classify(true, false); got != isolate.ClassMissingWorktree {
		t.Fatalf("a cell whose marker survives but worktree is gone must be MissingWorktree, got %q", got)
	}
}
