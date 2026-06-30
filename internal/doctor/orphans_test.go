package doctor_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/doctor"
	"github.com/ggkguelensan/workspace-isolation/internal/gc"
)

// TestDetectOrphans pins the orphan-inventory detector (DESIGN §7.5): it flags
// EXACTLY the cells gc.Classify calls ClassOrphanUnexplained — a markerless,
// non-live worktree — and nothing else. The non-orphan classes each have an owner
// that is NOT this detector, so each must produce zero findings:
//
//   - a live cell is HEAL-1's to reconcile (Classify short-circuits on Live, so a
//     markerless-but-live cell is NOT an orphan — the load-bearing safety case);
//   - a clean wi-owned leftover is gc's to reclaim, not a fault;
//   - a wi-owned work-carrying cell belongs to the drift detector.
func TestDetectOrphans(t *testing.T) {
	orphan := gc.Candidate{Task: "feat", Repo: "api"} // no marker, not live → orphan_unexplained
	liveMarkerless := gc.Candidate{Task: "feat", Repo: "web", Live: true}
	reclaimable := gc.Candidate{Task: "old", Repo: "api", HasMarker: true, Clean: true}
	blockedWork := gc.Candidate{Task: "wip", Repo: "api", HasMarker: true} // wi-owned, dirty

	got := doctor.DetectOrphans([]gc.Candidate{orphan, liveMarkerless, reclaimable, blockedWork})

	if len(got) != 1 {
		t.Fatalf("exactly the one orphan must be flagged, got %d findings: %+v", len(got), got)
	}
	f := got[0]
	if f.Kind != contract.KindConflict {
		t.Errorf("orphan finding Kind = %q, want %q (the loud refusal, exit 4)", f.Kind, contract.KindConflict)
	}
	if f.Code != "orphan_unexplained" {
		t.Errorf("orphan finding Code = %q, want %q", f.Code, "orphan_unexplained")
	}
	if f.Severity != doctor.SeverityError {
		t.Errorf("orphan_unexplained must be a LOUD ERROR finding, got severity %q", f.Severity)
	}
	if f.Repo != "api" || f.Task != "feat" {
		t.Errorf("orphan finding must carry cell identity, got task=%q repo=%q", f.Task, f.Repo)
	}
}

// TestDetectOrphansIsLoud composes the detector with WorstExit: because an orphan
// is an ERROR finding mapped to conflict, a workspace with any orphan makes a
// doctor run REFUSE (exit 4) — DESIGN §7.5's "orphan_unexplained is loud". An empty
// or orphan-free inventory is a clean exit 0.
//
// Registered non-vacuity mutant (DOCTOR-ORPHANS, loud limb): changing the orphan
// finding's Severity from SeverityError to SeverityWarning makes it exit-neutral,
// so WorstExit over an orphan-bearing inventory returns ExitOK instead of
// ExitRefused — reddening this test and the severity assertion above while the
// "exactly one finding" count stays green (a warning is still a finding). This
// pins that an orphan is LOUD, never a silently-tolerated advisory.
func TestDetectOrphansIsLoud(t *testing.T) {
	none := doctor.WorstExit(doctor.DetectOrphans(nil))
	if none != contract.ExitOK {
		t.Errorf("an empty inventory is a clean diagnosis, got exit %d", none)
	}

	orphans := doctor.DetectOrphans([]gc.Candidate{{Task: "feat", Repo: "api"}})
	if got := doctor.WorstExit(orphans); got != contract.ExitRefused {
		t.Errorf("an orphan must make doctor refuse (exit %d), got exit %d", contract.ExitRefused, got)
	}
}

// TestDetectOrphansFlagsOnlyOrphanClass isolates the class selection: the detector
// must report ONLY ClassOrphanUnexplained, never every non-live cell. A clean
// wi-owned leftover (reclaimable) and a wi-owned work-carrying cell (blocked_work)
// are real states a workspace carries normally — flagging them as orphans would be
// a false loud refusal on cells that have a legitimate owner.
//
// Registered non-vacuity mutant (DOCTOR-ORPHANS, class limb): replacing the filter
//
//	if gc.Classify(c) != gc.ClassOrphanUnexplained { continue }
//
// with `if gc.Classify(c) == gc.ClassLive { continue }` (flag every NON-live class)
// makes the reclaimable and blocked_work cells produce spurious orphan findings →
// this test RED, while the genuine-orphan and live rows stay green — pinning that
// doctor selects gc's specific orphan verdict, not a coarse live/non-live split.
func TestDetectOrphansFlagsOnlyOrphanClass(t *testing.T) {
	nonOrphans := []gc.Candidate{
		{Task: "a", Repo: "r", Live: true},                         // live
		{Task: "b", Repo: "r", HasMarker: true, Clean: true},       // reclaimable
		{Task: "c", Repo: "r", HasMarker: true},                    // blocked_work (dirty)
		{Task: "d", Repo: "r", HasMarker: true, AheadOfBase: true}, // blocked_work (ahead)
	}
	if got := doctor.DetectOrphans(nonOrphans); len(got) != 0 {
		t.Fatalf("no non-orphan class may be flagged as an orphan, got %d findings: %+v", len(got), got)
	}
}
