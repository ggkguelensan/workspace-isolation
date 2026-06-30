package journal_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/journal"
)

// Guard HEAL-CRASH-RECOVER (classifier limb) — the pure recovery classifier of HEAL-4
// (DESIGN §7.4, PLAN 76-77), the decision core the offline roll-forward recovery scan
// builds on, exactly as gc.Classify is the pure verdict before gc.Collect acts. Given
// the entries of ONE op's journal it returns the op's recovery Disposition from its
// FURTHEST-REACHED phase (decision #4: roll FORWARD, never back):
//
//   - done furthest      → Complete     — the op finished cleanly; nothing to recover
//                                          (its journal is stale and may be cleaned up).
//   - committed furthest → RollForward  — the op crossed its point of no return but never
//                                          wrote done; recovery FINISHES it (never undoes
//                                          it — an interrupted isolate-rm completes the
//                                          deletion, it is not restored). THE load-bearing
//                                          guarantee: a crashed-after-commit op must finish.
//   - intent furthest    → Abandoned    — the op crashed BEFORE committing (before the
//                                          point of no return); recovery neither finishes
//                                          it (nothing durably began) nor undoes it
//                                          (roll-forward-only) — its partial artifacts, if
//                                          any, are reconciled evidence-positively by
//                                          isolate repair (HEAL-1) / gc (HEAL-2).
//
// Disposition is an INTERNAL recovery vocabulary (like Phase/Kind and gc.Class), not a
// closed wire enum agents parse — contract stays the sole owner of those.
//
// Non-vacuity (registered): (primary — roll-forward guarantee, the resolution of decision
// #4) map PhaseCommitted to DispositionComplete instead of DispositionRollForward → a
// committed-but-not-done op is silently treated as finished and never recovered →
// TestClassifyByFurthestPhase RED on the committed rows. (alternate — furthest-wins) read
// the LAST entry's phase instead of the furthest-reached one → TestClassifyFurthestWins
// RED on a non-monotonic journal (a stray earlier-phase line after a later one must not
// downgrade the verdict).

func entries(opID string, phases ...journal.Phase) []journal.Entry {
	out := make([]journal.Entry, 0, len(phases))
	for _, p := range phases {
		out = append(out, journal.Entry{OpID: opID, Kind: journal.KindIsolateRm, Phase: p, Task: "feat"})
	}
	return out
}

func TestClassifyByFurthestPhase(t *testing.T) {
	cases := []struct {
		name   string
		phases []journal.Phase
		want   journal.Disposition
	}{
		{"intent only → abandoned", []journal.Phase{journal.PhaseIntent}, journal.DispositionAbandoned},
		{"intent+committed → roll forward", []journal.Phase{journal.PhaseIntent, journal.PhaseCommitted}, journal.DispositionRollForward},
		{"full lifecycle → complete", []journal.Phase{journal.PhaseIntent, journal.PhaseCommitted, journal.PhaseDone}, journal.DispositionComplete},
		{"committed without intent line → roll forward", []journal.Phase{journal.PhaseCommitted}, journal.DispositionRollForward},
		{"done without committed line → complete", []journal.Phase{journal.PhaseIntent, journal.PhaseDone}, journal.DispositionComplete},
		{"done only → complete", []journal.Phase{journal.PhaseDone}, journal.DispositionComplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := journal.Classify(entries("op_x", tc.phases...))
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got != tc.want {
				t.Errorf("Classify(%v) = %q, want %q", tc.phases, got, tc.want)
			}
		})
	}
}

// TestClassifyFurthestWins pins that the verdict is the FURTHEST-reached phase, not the
// last-written line: a torn or re-ordered journal where an earlier-phase line trails a
// later one must NOT downgrade the verdict (which, for a committed op, would silently drop
// it from recovery — a data-integrity bug).
func TestClassifyFurthestWins(t *testing.T) {
	cases := []struct {
		name   string
		phases []journal.Phase
		want   journal.Disposition
	}{
		{"committed then stray intent → still roll forward", []journal.Phase{journal.PhaseCommitted, journal.PhaseIntent}, journal.DispositionRollForward},
		{"done then stray intent → still complete", []journal.Phase{journal.PhaseDone, journal.PhaseIntent}, journal.DispositionComplete},
		{"done then stray committed → still complete", []journal.Phase{journal.PhaseDone, journal.PhaseCommitted}, journal.DispositionComplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := journal.Classify(entries("op_x", tc.phases...))
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got != tc.want {
				t.Errorf("Classify(%v) = %q, want %q (furthest phase must win, not last entry)", tc.phases, got, tc.want)
			}
		})
	}
}

// TestClassifyEmptyIsError pins that an empty entry set (a 0-byte or contentless journal
// file — an anomaly, never produced by Append) is surfaced as an error, never silently
// classified as a recoverable or complete op.
func TestClassifyEmptyIsError(t *testing.T) {
	if got, err := journal.Classify(nil); err == nil {
		t.Errorf("Classify(nil) = (%q, nil), want an error", got)
	}
}
