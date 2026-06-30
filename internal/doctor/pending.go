package doctor

import (
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
)

// DetectPendingOps is the journal half of doctor's "pending journal/parked ops"
// battery (DESIGN §7.5), HEAL-8 detector #3. Like every doctor detector it is a
// PURE function from injected observations to []Finding: here the observations
// are the recovery worklist journal.Scan already produced from <root>/.wi/journal
// (the doctor command runs Scan; a journal Scan CANNOT parse — a torn line or a
// contentless file — is a separate concern of the `.wi` parseability detector and
// surfaces as KindInternal, not here). No I/O happens in this function.
//
// It REUSES the journal Disposition verdict (journal.Classify, surfaced on each
// OpRecovery) rather than re-deriving "did this op finish" from raw lifecycle
// phases — journal is the sole owner of that verdict, the same way gc owns the
// orphan verdict (DOCTOR-ORPHANS) and mirror owns the staleness verdict
// (DOCTOR-MIRROR). doctor diagnoses pending work with the same eyes offline
// recovery acts with, so a future `--fix` (which drains the journal via the same
// recovery path) can never disagree with what doctor reported.
//
// Mapping: any op whose furthest-reached phase is NOT `done` is pending recovery
// work, mapped to a KindPartial ERROR finding — exit 2, the MILDEST non-ok in the
// #DOCTOR-EXIT-WORST rank ("a known, resumable pending op — the workspace is
// sound, an op just needs draining"). A pending op is a real (if mild) ERROR, not
// a WARNING: unlike a stale mirror (benign and expected until you sync), an
// unfinished op means the workspace is not fully clean, so it must move the exit
// off 0. The two non-done dispositions carry distinct sub-codes:
//
//   - roll_forward (committed, not done): offline recovery will finish it →
//     op_roll_forward_pending;
//   - abandoned (intent only, crashed pre-commit): recovery discards its journal
//     entry and the evidence-positive heals reclaim any debris → op_abandoned.
//
// A `complete` op (reached done, journal entry not yet discarded) is finished —
// nothing pending — and produces no finding.
//
// Guard DOCTOR-PENDING.
func DetectPendingOps(ops []journal.OpRecovery) []Finding {
	var out []Finding
	for _, op := range ops {
		if op.Disposition == journal.DispositionComplete {
			continue // reached `done` — nothing pending
		}
		code := "op_abandoned"
		detail := "crashed before committing — offline recovery will discard its journal entry and the evidence-positive heals reclaim any debris"
		if op.Disposition == journal.DispositionRollForward {
			code = "op_roll_forward_pending"
			detail = "committed but not finished — offline recovery will roll it forward to done on next startup"
		}
		out = append(out, Finding{
			Detector: "journal",
			Kind:     contract.KindPartial,
			Code:     code,
			Severity: SeverityError,
			Message: fmt.Sprintf(
				"pending %s operation %s (task %q): %s",
				op.Kind, op.OpID, op.Task, detail),
			Task: op.Task,
		})
	}
	return out
}
