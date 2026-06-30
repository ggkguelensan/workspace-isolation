// This file is the pure recovery classifier of HEAL-4 (DESIGN §7.4, PLAN 76-77): given
// one operation's journal entries it returns the op's recovery Disposition from its
// FURTHEST-reached phase. It is the decision core the offline roll-forward recovery scan
// is built on — pure, total over a non-empty entry set, no I/O, no network — exactly as
// gc.Classify is the verdict gc.Collect then acts on.
//
// The policy (decision #4, resolved here — roll FORWARD, never back):
//   - an op that reached `done` is COMPLETE — there is nothing to recover;
//   - an op that reached `committed` but not `done` crossed its point of no return and is
//     ROLLED FORWARD: recovery FINISHES it (an interrupted isolate-rm completes its
//     deletion; it is never restored — an interrupted remove cannot be undone by re-run);
//   - an op that only reached `intent` crashed BEFORE committing: recovery ABANDONS it —
//     it neither finishes it (nothing durably began) nor undoes it (there is no roll-back),
//     leaving any partial artifacts to the evidence-positive heals (isolate repair, gc).
//
// Disposition is an INTERNAL recovery vocabulary, like Phase/Kind and gc.Class — not a
// closed wire enum agents parse (internal/contract remains the sole owner of those).
package journal

import "fmt"

// Disposition is the recovery action an op's journal calls for, decided from its
// furthest-reached phase.
type Disposition string

const (
	// DispositionComplete: the op reached `done` — nothing to recover.
	DispositionComplete Disposition = "complete"
	// DispositionRollForward: the op reached `committed` but not `done` — recovery
	// finishes it (never undoes it).
	DispositionRollForward Disposition = "roll_forward"
	// DispositionAbandoned: the op only reached `intent` — it crashed before committing;
	// recovery neither finishes nor undoes it, leaving partials to the evidence-positive heals.
	DispositionAbandoned Disposition = "abandoned"
)

// phaseRank orders the lifecycle phases so "furthest reached" is a max over entries:
// intent < committed < done. Every entry has been validated (Phase.Valid), so an
// out-of-set phase cannot reach here; it would rank 0, below intent, and so never wins.
func phaseRank(p Phase) int {
	switch p {
	case PhaseIntent:
		return 1
	case PhaseCommitted:
		return 2
	case PhaseDone:
		return 3
	default:
		return 0
	}
}

// Classify returns the recovery Disposition for one op from the FURTHEST phase any of its
// entries reached — not the last line written, so a torn or re-ordered journal (a stray
// earlier-phase line trailing a later one) can never downgrade the verdict and drop a
// committed op from recovery. The entries must all belong to one op (the scan layer reads
// them per op_id file). An empty set is an anomaly — a contentless journal file, never
// produced by Append — and is surfaced as an error rather than silently classified.
func Classify(entries []Entry) (Disposition, error) {
	if len(entries) == 0 {
		return "", fmt.Errorf("journal: classify: no entries")
	}
	furthest := entries[0].Phase
	for _, e := range entries[1:] {
		if phaseRank(e.Phase) > phaseRank(furthest) {
			furthest = e.Phase
		}
	}
	switch furthest {
	case PhaseDone:
		return DispositionComplete, nil
	case PhaseCommitted:
		return DispositionRollForward, nil
	default: // PhaseIntent
		return DispositionAbandoned, nil
	}
}
