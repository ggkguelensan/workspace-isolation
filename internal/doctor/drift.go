package doctor

import (
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
)

// DriftObservation pairs one observed isolate cell with the task that owns it.
// The doctor command builds the slice by enumerating the registry (state.List
// gives the live tasks) and running isolate.Inspect per task — a read-only,
// offline observation (marker-ref read + worktree stat) — then tagging each
// returned Cell with that record's task. The detector itself does NO I/O; it
// receives the cells already observed, like every other doctor detector.
type DriftObservation struct {
	Task string
	Cell isolate.Cell
}

// DetectDrift is the three-way isolate-drift detector (DESIGN §7.5, HEAL-8
// detector #5): for each observed isolate cell it reduces the (recorded stage,
// owned marker, worktree on disk) drift to a Finding. Like every doctor detector
// it is a PURE function from injected observations to []Finding — no I/O.
//
// REUSE move (the canonical pattern: DOCTOR-ORPHANS↔gc.Classify,
// DOCTOR-MIRROR↔mirror.Freshness, DOCTOR-PENDING↔journal.Classify,
// DOCTOR-PARKED-LANDS↔landstate.Parked): it consumes isolate.PlanAction — the
// reconciler's pure per-cell verdict, the EXACT decision `isolate repair` (HEAL-1,
// the SAFE-tier heal a future `--fix` dispatches) acts on — rather than
// re-deriving "what kind of drift is this" from the raw Class+Stage. isolate owns
// that verdict, so doctor diagnoses drift with the same eyes the heal acts with: a
// `--fix → isolate repair` can never disagree with what doctor reported.
//
// Mapping (decision #DOCTOR-DRIFT-KIND, revised — see PROGRESS.md). The isolate
// reconciler's ONLY hard block is RepairBlockOrphan (an unexplained orphan
// worktree); every OTHER drift it can heal losslessly. So every drift finding
// rides the MILD frozen Kind KindPartial (exit 2 — "the workspace is sound, a safe
// heal just needs to run", the same rank pending journal ops and parked lands
// carry); the conflict/exit-4 rank is reserved for the genuine hard-block, which
// this detector DEFERS to its single owner:
//
//   - RepairRematerialize (ClassMissingWorktree — marker survives, worktree gone)
//     → ERROR drift_missing_worktree. Losslessly re-materializable at the marker's
//     recorded base, so a mild resumable error, not a hard block.
//   - RepairDropRecord (ClassReclaimed — neither marker nor worktree) → ERROR
//     drift_stale_record. A stale registry tombstone the repair drops.
//   - RepairHealStage (ClassConsistent — but the recorded stage lags at pending)
//     → WARNING drift_stage_lag. The worktree is already correct; only the
//     registry stage trails a completed materialize, so it is exit-neutral (via
//     WorstExit a WARNING never moves the exit) — a benign cosmetic lag.
//   - RepairNone (the healthy steady state) → NO finding.
//   - RepairBlockOrphan (ClassOrphanWorktree — worktree wi cannot prove it owns)
//     → NO finding HERE. This is the load-bearing skip: orphan_unexplained is the
//     single most dangerous class and is reported by DOCTOR-ORPHANS (gc.Classify
//     owns the §7.1 evidence-positive verdict). Re-reporting it here would
//     double-count it and risk a --fix disagreeing about who owns the heal.
//
// Guard DOCTOR-DRIFT.
func DetectDrift(obs []DriftObservation) []Finding {
	var out []Finding
	for _, o := range obs {
		switch isolate.PlanAction(o.Cell) {
		case isolate.RepairRematerialize:
			out = append(out, Finding{
				Detector: "isolate",
				Kind:     contract.KindPartial,
				Code:     "drift_missing_worktree",
				Severity: SeverityError,
				Message: fmt.Sprintf(
					"isolate %q repo %q: worktree is gone but the wi-owned marker survives — run `wi isolate repair %s` to re-materialize it at its recorded base",
					o.Task, o.Cell.Repo, o.Task),
				Repo: o.Cell.Repo,
				Task: o.Task,
			})
		case isolate.RepairDropRecord:
			out = append(out, Finding{
				Detector: "isolate",
				Kind:     contract.KindPartial,
				Code:     "drift_stale_record",
				Severity: SeverityError,
				Message: fmt.Sprintf(
					"isolate %q repo %q: a registry record survives but the cell was reclaimed (no marker, no worktree) — run `wi isolate repair %s` to drop the stale record",
					o.Task, o.Cell.Repo, o.Task),
				Repo: o.Cell.Repo,
				Task: o.Task,
			})
		case isolate.RepairHealStage:
			out = append(out, Finding{
				Detector: "isolate",
				Kind:     contract.KindPartial,
				Code:     "drift_stage_lag",
				Severity: SeverityWarning, // benign: the worktree is correct, only the registry stage lags — exit-neutral
				Message: fmt.Sprintf(
					"isolate %q repo %q: the worktree is materialized but the registry stage still reads pending — `wi isolate repair %s` heals the lagging stage (no data at risk)",
					o.Task, o.Cell.Repo, o.Task),
				Repo: o.Cell.Repo,
				Task: o.Task,
			})
		case isolate.RepairNone, isolate.RepairBlockOrphan:
			// RepairNone: healthy steady state — nothing to report. RepairBlockOrphan:
			// an unexplained orphan worktree — DELIBERATELY skipped so orphan_unexplained
			// stays single-sourced in DOCTOR-ORPHANS (gc.Classify), never double-counted.
			continue
		}
	}
	return out
}
