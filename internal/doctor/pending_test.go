package doctor_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/doctor"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
)

// TestDetectPendingOps pins the journal pending-ops detector (DESIGN §7.5): it
// flags EXACTLY the ops whose journal disposition is NOT `complete` — a
// roll_forward op (committed, not done) and an abandoned op (intent only) — each
// as a KindPartial ERROR carrying its distinct sub-code, and excludes the
// `complete` op (reached done — nothing pending).
func TestDetectPendingOps(t *testing.T) {
	complete := journal.OpRecovery{OpID: "op_done", Kind: journal.KindLand, Task: "feat", Disposition: journal.DispositionComplete}
	rollFwd := journal.OpRecovery{OpID: "op_rf", Kind: journal.KindLand, Task: "wip", Disposition: journal.DispositionRollForward}
	abandoned := journal.OpRecovery{OpID: "op_ab", Kind: journal.KindIsolateNew, Task: "exp", Disposition: journal.DispositionAbandoned}

	got := doctor.DetectPendingOps([]journal.OpRecovery{complete, rollFwd, abandoned})

	if len(got) != 2 {
		t.Fatalf("exactly the two non-done ops must be flagged, got %d findings: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Kind != contract.KindPartial {
			t.Errorf("pending-op finding Kind = %q, want %q (the mildest non-ok)", f.Kind, contract.KindPartial)
		}
		if f.Severity != doctor.SeverityError {
			t.Errorf("a pending op must be an ERROR finding (it moves the exit off 0), got severity %q for %s", f.Severity, f.Code)
		}
	}
	// The two findings are the roll-forward and the abandoned op, in worklist order.
	if got[0].Code != "op_roll_forward_pending" || got[0].Task != "wip" {
		t.Errorf("first finding = (code %q, task %q), want (op_roll_forward_pending, wip)", got[0].Code, got[0].Task)
	}
	if got[1].Code != "op_abandoned" || got[1].Task != "exp" {
		t.Errorf("second finding = (code %q, task %q), want (op_abandoned, exp)", got[1].Code, got[1].Task)
	}
}

// TestDetectPendingOpsIsPartial composes the detector with WorstExit: a pending
// op makes a doctor run exit 2 (partial) — the workspace is sound but not fully
// clean, an op just needs draining. An empty worklist is a clean exit 0.
//
// Registered non-vacuity mutant (DOCTOR-PENDING, severity limb): changing the
// finding's Severity from SeverityError to SeverityWarning makes a pending op
// exit-neutral, so WorstExit returns ExitOK instead of ExitPartial — reddening
// this test and the severity assertion in TestDetectPendingOps, while the
// all-complete count test (TestDetectPendingOpsFlagsOnlyIncomplete) stays green.
// This pins that a pending op is a real (if mild) error, not a mere advisory.
func TestDetectPendingOpsIsPartial(t *testing.T) {
	none := doctor.WorstExit(doctor.DetectPendingOps(nil))
	if none != contract.ExitOK {
		t.Errorf("an empty worklist is a clean diagnosis, got exit %d", none)
	}

	pending := doctor.DetectPendingOps([]journal.OpRecovery{
		{OpID: "op_rf", Kind: journal.KindLand, Task: "wip", Disposition: journal.DispositionRollForward},
	})
	if got := doctor.WorstExit(pending); got != contract.ExitPartial {
		t.Errorf("a pending op must make doctor exit partial (%d), got exit %d", contract.ExitPartial, got)
	}
}

// TestDetectPendingOpsFlagsOnlyIncomplete isolates the selection: the detector
// must report ONLY ops that did not reach done, never a `complete` op (which has
// finished and merely awaits journal cleanup). An all-complete worklist yields
// zero findings.
//
// Registered non-vacuity mutant (DOCTOR-PENDING, selection limb): deleting the
//
//	if op.Disposition == journal.DispositionComplete { continue }
//
// skip makes completed ops fall through and emit spurious pending findings → this
// test RED (findings appear where none should) and TestDetectPendingOps's count
// RED (3 not 2), while TestDetectPendingOpsIsPartial stays green (a lone
// roll-forward op still maps to exit partial). This pins that doctor selects
// journal's specific not-done verdict, not every op in the worklist.
func TestDetectPendingOpsFlagsOnlyIncomplete(t *testing.T) {
	allComplete := []journal.OpRecovery{
		{OpID: "op_a", Kind: journal.KindLand, Task: "a", Disposition: journal.DispositionComplete},
		{OpID: "op_b", Kind: journal.KindIsolateRm, Task: "b", Disposition: journal.DispositionComplete},
	}
	if got := doctor.DetectPendingOps(allComplete); len(got) != 0 {
		t.Fatalf("no completed op may be flagged as pending, got %d findings: %+v", len(got), got)
	}
}
