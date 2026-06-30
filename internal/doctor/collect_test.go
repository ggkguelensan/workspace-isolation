package doctor_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/doctor"
	"github.com/ggkguelensan/workspace-isolation/internal/gc"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/mirror"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// TestCollectRunsEveryDetector pins doctor.Collect as the COMPLETE detector
// battery (DESIGN §7.5, HEAL-8): one Observations bundle whose every field
// carries a fixture known to fire ≥1 finding must come back with a finding from
// EVERY detector — the exact set of Detector values {orphans, mirror, journal,
// land, isolate}. Collect is the single seam the `wi doctor` command calls
// (gather Observations via I/O → Collect → WorstExit), so a detector silently
// dropped from the battery would make `wi doctor` stop diagnosing that whole
// trouble class while still exiting 0 — exactly what this test forbids.
//
// Each field reuses the trigger case its own detector test already pins:
//   - orphans : a markerless, non-live gc.Candidate (orphan_unexplained, ERROR)
//   - mirror  : a Snapshot behind origin (mirror_stale, WARNING)
//   - journal : a non-complete OpRecovery (op_roll_forward_pending, ERROR)
//   - land    : a TaskLand with a blocked repo (land_parked, ERROR)
//   - isolate : a ClassMissingWorktree drift cell (drift_missing_worktree, ERROR)
//
// Registered non-vacuity mutants (DOCTOR-BATTERY-COMPLETE, two-sided): deleting
// the DetectMirrorStaleness(obs.Mirrors) append from Collect drops the "mirror"
// finding → the membership assertion for "mirror" RED; independently, deleting
// the DetectDrift(obs.Drift) append drops "isolate" → the "isolate" assertion
// RED. Either dropped detector reddens this test, pinning that Collect runs the
// WHOLE battery, not a subset (and that a forgotten detector cannot hide behind
// a clean exit). Before Collect/Observations exist the package fails to compile
// (compile RED — the symbols are absent).
func TestCollectRunsEveryDetector(t *testing.T) {
	obs := doctor.Observations{
		Orphans: []gc.Candidate{
			{Task: "feat", Repo: "api"}, // no marker, not live → orphan_unexplained
		},
		Mirrors: []mirror.Snapshot{
			{Repo: "api", BehindOriginAsOfFetch: 3}, // behind origin → mirror_stale
		},
		PendingOps: []journal.OpRecovery{
			{OpID: "op_rf", Kind: journal.KindLand, Task: "wip", Disposition: journal.DispositionRollForward},
		},
		ParkedLands: []landstate.TaskLand{
			{Task: "wip", OpID: "op_b", Repos: []landstate.RepoLand{{Repo: "api", Phase: landstate.PhaseBlocked}}},
		},
		Drift: []doctor.DriftObservation{
			{Task: "feat", Cell: isolate.Cell{Repo: "api", Class: isolate.ClassMissingWorktree, Stage: state.StageCreated}},
		},
	}

	seen := map[string]bool{}
	for _, f := range doctor.Collect(obs) {
		seen[f.Detector] = true
	}

	for _, det := range []string{"orphans", "mirror", "journal", "land", "isolate"} {
		if !seen[det] {
			t.Errorf("Collect dropped the %q detector: no finding carried that Detector — the battery is incomplete (saw %v)", det, seen)
		}
	}
}
