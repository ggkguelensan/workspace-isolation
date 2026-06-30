package doctor_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/doctor"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
)

// TestDetectParkedLands pins the parked-land half of doctor's "pending
// journal/parked ops" battery (DESIGN §7.5): it flags EXACTLY the durable
// land records that are still parked — at least one repo has not reached
// PhaseLanded (a blocked repo, or a repo a crash left pending) — each as a
// KindPartial ERROR carrying the land_parked sub-code, and excludes a land
// whose every repo is PhaseLanded (finished, kept only for the abort window)
// and an empty record (no cells ⟹ nothing to finish).
func TestDetectParkedLands(t *testing.T) {
	blocked := landstate.TaskLand{Task: "feat", OpID: "op_a", Repos: []landstate.RepoLand{
		{Repo: "api", Phase: landstate.PhaseLanded},
		{Repo: "web", Phase: landstate.PhaseBlocked},
	}}
	pending := landstate.TaskLand{Task: "wip", OpID: "op_b", Repos: []landstate.RepoLand{
		{Repo: "api", Phase: landstate.PhasePending},
	}}
	clean := landstate.TaskLand{Task: "done", OpID: "op_c", Repos: []landstate.RepoLand{
		{Repo: "api", Phase: landstate.PhaseLanded},
		{Repo: "web", Phase: landstate.PhaseLanded},
	}}
	empty := landstate.TaskLand{Task: "fresh", OpID: "op_d"}

	got := doctor.DetectParkedLands([]landstate.TaskLand{blocked, pending, clean, empty})

	if len(got) != 2 {
		t.Fatalf("exactly the two parked lands must be flagged, got %d findings: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Kind != contract.KindPartial {
			t.Errorf("parked-land finding Kind = %q, want %q (the mildest non-ok, exit 2)", f.Kind, contract.KindPartial)
		}
		if f.Code != "land_parked" {
			t.Errorf("parked-land finding Code = %q, want %q", f.Code, "land_parked")
		}
		if f.Severity != doctor.SeverityError {
			t.Errorf("a parked land must be an ERROR finding (it moves the exit off 0), got severity %q for task %q", f.Severity, f.Task)
		}
	}
	// The two findings are the blocked land and the crash-pending land, in record order.
	if got[0].Task != "feat" {
		t.Errorf("first finding task = %q, want %q (the blocked land)", got[0].Task, "feat")
	}
	if got[1].Task != "wip" {
		t.Errorf("second finding task = %q, want %q (the crash-pending land)", got[1].Task, "wip")
	}
}

// TestDetectParkedLandsIsPartial composes the detector with WorstExit: a parked
// land makes a doctor run exit 2 (partial) — the same resumable "pending op"
// signal the journal detector gives an unfinished op; the workspace is sound, a
// land just needs `land continue`/`land abort`. An empty inventory is a clean
// exit 0.
//
// Registered non-vacuity mutant (DOCTOR-PARKED-LANDS, severity limb): changing
// the finding's Severity from SeverityError to SeverityWarning makes a parked
// land exit-neutral, so WorstExit over a parked inventory returns ExitOK instead
// of ExitPartial — reddening this test and the severity assertion in
// TestDetectParkedLands, while TestDetectParkedLandsFlagsOnlyParked's count stays
// green (a warning is still a finding). This pins that a parked land is a real
// (if mild) error, not a mere advisory.
func TestDetectParkedLandsIsPartial(t *testing.T) {
	none := doctor.WorstExit(doctor.DetectParkedLands(nil))
	if none != contract.ExitOK {
		t.Errorf("an empty land inventory is a clean diagnosis, got exit %d", none)
	}

	parked := doctor.DetectParkedLands([]landstate.TaskLand{
		{Task: "wip", OpID: "op_b", Repos: []landstate.RepoLand{{Repo: "api", Phase: landstate.PhaseBlocked}}},
	})
	if got := doctor.WorstExit(parked); got != contract.ExitPartial {
		t.Errorf("a parked land must make doctor exit partial (%d), got exit %d", contract.ExitPartial, got)
	}
}

// TestDetectParkedLandsFlagsOnlyParked isolates the selection: the detector must
// report ONLY lands that are still parked, never a fully-landed record (finished,
// retained only for the abort window) or an empty record. A worklist of only
// those yields zero findings.
//
// Registered non-vacuity mutant (DOCTOR-PARKED-LANDS, selection limb): deleting
// the
//
//	if !rec.Parked() { continue }
//
// skip makes finished and empty lands fall through and emit spurious parked
// findings → this test RED (findings appear where none should) and
// TestDetectParkedLands's count RED (4 not 2), while TestDetectParkedLandsIsPartial
// stays green (a lone blocked land still maps to exit partial). This pins that
// doctor selects landstate's specific Parked() verdict, not every land record.
func TestDetectParkedLandsFlagsOnlyParked(t *testing.T) {
	settled := []landstate.TaskLand{
		{Task: "done", OpID: "op_c", Repos: []landstate.RepoLand{
			{Repo: "api", Phase: landstate.PhaseLanded},
			{Repo: "web", Phase: landstate.PhaseLanded},
		}},
		{Task: "fresh", OpID: "op_d"},
	}
	if got := doctor.DetectParkedLands(settled); len(got) != 0 {
		t.Fatalf("no settled land may be flagged as parked, got %d findings: %+v", len(got), got)
	}
}
