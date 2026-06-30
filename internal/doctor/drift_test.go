package doctor_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/doctor"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// TestDetectDrift pins the three-way isolate-drift detector (DESIGN §7.5,
// detector #5): for each observed isolate cell it consumes the SAME verdict
// `isolate repair` (HEAL-1) acts on — isolate.PlanAction — and maps the
// AUTO-HEALABLE drifts to findings, while leaving the healthy steady state and
// the one HARD-block class (an unexplained orphan worktree) unflagged here.
//
//   - RepairRematerialize (ClassMissingWorktree: marker survives, worktree gone)
//     → ERROR drift_missing_worktree (KindPartial, exit 2 — losslessly
//     re-materializable, so the mild "a heal needs to run" rank, not a hard block).
//   - RepairDropRecord (ClassReclaimed: neither marker nor worktree) → ERROR
//     drift_stale_record (KindPartial, exit 2 — a stale registry tombstone the
//     repair drops).
//   - RepairHealStage (ClassConsistent but the stage lags at pending) → WARNING
//     drift_stage_lag (exit-neutral — the worktree is already correct; only the
//     registry stage trails a completed materialize).
//   - RepairNone (healthy) and RepairBlockOrphan (ClassOrphanWorktree) → NO
//     finding. The orphan is deliberately NOT reported here so orphan_unexplained
//     stays single-sourced in DOCTOR-ORPHANS (gc.Classify owns it).
func TestDetectDrift(t *testing.T) {
	obs := []doctor.DriftObservation{
		{Task: "feat", Cell: isolate.Cell{Repo: "api", Class: isolate.ClassMissingWorktree, Stage: state.StageCreated}},
		{Task: "feat", Cell: isolate.Cell{Repo: "web", Class: isolate.ClassReclaimed, Stage: state.StageCreated}},
		{Task: "wip", Cell: isolate.Cell{Repo: "api", Class: isolate.ClassConsistent, Stage: state.StagePending}},
		{Task: "wip", Cell: isolate.Cell{Repo: "web", Class: isolate.ClassConsistent, Stage: state.StageCreated}}, // healthy
		{Task: "ghost", Cell: isolate.Cell{Repo: "api", Class: isolate.ClassOrphanWorktree, Stage: state.StageCreated}},
	}

	got := doctor.DetectDrift(obs)

	if len(got) != 3 {
		t.Fatalf("exactly the three drift cells (missing, reclaimed, stage-lag) must be flagged; healthy + orphan are skipped — got %d: %+v", len(got), got)
	}
	// Every drift finding rides the same frozen Kind (KindPartial) and is the
	// isolate detector's — the sub-Code and Severity carry the granularity.
	for _, f := range got {
		if f.Detector != "isolate" {
			t.Errorf("drift finding Detector = %q, want %q", f.Detector, "isolate")
		}
		if f.Kind != contract.KindPartial {
			t.Errorf("drift finding %q Kind = %q, want %q (all drift sub-codes ride KindPartial; the one hard-block class is owned by DOCTOR-ORPHANS)", f.Code, f.Kind, contract.KindPartial)
		}
	}
	type want struct {
		code     string
		severity doctor.Severity
		repo     string
		task     string
	}
	wants := []want{
		{"drift_missing_worktree", doctor.SeverityError, "api", "feat"},
		{"drift_stale_record", doctor.SeverityError, "web", "feat"},
		{"drift_stage_lag", doctor.SeverityWarning, "api", "wip"},
	}
	for i, w := range wants {
		if got[i].Code != w.code {
			t.Errorf("finding[%d].Code = %q, want %q", i, got[i].Code, w.code)
		}
		if got[i].Severity != w.severity {
			t.Errorf("finding[%d] (%s).Severity = %q, want %q", i, w.code, got[i].Severity, w.severity)
		}
		if got[i].Repo != w.repo || got[i].Task != w.task {
			t.Errorf("finding[%d] (%s) identity = {repo %q, task %q}, want {repo %q, task %q}", i, w.code, got[i].Repo, got[i].Task, w.repo, w.task)
		}
	}
}

// TestDetectDriftExit composes the detector with WorstExit: an auto-healable
// drift (a missing worktree) makes a doctor run exit 2 (partial) — the same
// resumable "a safe heal needs to run" signal the journal/parked detectors give —
// while a stage-lag WARNING is exit-neutral (a clean exit 0). An empty inventory
// is exit 0.
//
// Registered non-vacuity mutant (DOCTOR-DRIFT, severity limb): change the
// RepairRematerialize arm's Severity from SeverityError to SeverityWarning → a
// missing-worktree drift becomes exit-neutral → WorstExit over it returns ExitOK
// instead of ExitPartial, reddening this test and the Severity assertion in
// TestDetectDrift, while TestDetectDriftSkipsOrphanAndHealthy's count stays green
// (a warning is still no finding for the skipped classes). Pins that a missing
// worktree is a real (if mild) error that moves the exit off 0.
func TestDetectDriftExit(t *testing.T) {
	if none := doctor.WorstExit(doctor.DetectDrift(nil)); none != contract.ExitOK {
		t.Errorf("an empty drift inventory is a clean diagnosis, got exit %d", none)
	}

	missing := doctor.DetectDrift([]doctor.DriftObservation{
		{Task: "feat", Cell: isolate.Cell{Repo: "api", Class: isolate.ClassMissingWorktree, Stage: state.StageCreated}},
	})
	if got := doctor.WorstExit(missing); got != contract.ExitPartial {
		t.Errorf("a missing-worktree drift must make doctor exit partial (%d), got exit %d", contract.ExitPartial, got)
	}

	stageLag := doctor.DetectDrift([]doctor.DriftObservation{
		{Task: "wip", Cell: isolate.Cell{Repo: "api", Class: isolate.ClassConsistent, Stage: state.StagePending}},
	})
	if got := doctor.WorstExit(stageLag); got != contract.ExitOK {
		t.Errorf("a stage-lag drift is a WARNING and must be exit-neutral (0), got exit %d", got)
	}
}

// TestDetectDriftSkipsOrphanAndHealthy isolates the selection: the detector must
// emit NO finding for a healthy cell OR for an unexplained orphan worktree. The
// orphan is the load-bearing skip — orphan_unexplained is reported by
// DOCTOR-ORPHANS (gc.Classify), so re-reporting it here would double-count the
// single most dangerous class and risk a --fix disagreeing about who owns it.
//
// Registered non-vacuity mutant (DOCTOR-DRIFT, selection limb): make the
// RepairBlockOrphan case emit a Finding instead of `continue` → an orphan worktree
// produces a spurious drift finding → this test RED (findings where none should be)
// and TestDetectDrift's count RED (4 not 3), while TestDetectDriftExit stays green
// (a lone missing-worktree still maps to exit 2). Pins that drift defers the
// orphan hard-block to its single owner.
func TestDetectDriftSkipsOrphanAndHealthy(t *testing.T) {
	settled := []doctor.DriftObservation{
		{Task: "wip", Cell: isolate.Cell{Repo: "web", Class: isolate.ClassConsistent, Stage: state.StageCreated}},
		{Task: "ghost", Cell: isolate.Cell{Repo: "api", Class: isolate.ClassOrphanWorktree, Stage: state.StageCreated}},
	}
	if got := doctor.DetectDrift(settled); len(got) != 0 {
		t.Fatalf("a healthy cell and an unexplained orphan must yield no drift finding (orphan is DOCTOR-ORPHANS'), got %d: %+v", len(got), got)
	}
}
