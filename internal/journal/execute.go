// This file is the offline roll-forward executor of HEAL-4 (DESIGN §7.4, PLAN
// 76-77, decision #4). journal.Recover scans the journal subtree (Scan, sub-unit
// 3b) and enacts each op's recovery Disposition (Classify, sub-unit 3a):
//
//   - RollForward — the op crossed its point of no return (committed) but never
//     wrote done: run the injected Finisher (its durable completion — an
//     interrupted isolate-rm finishes its deletion), append a `done` entry to
//     honestly close the lifecycle, then remove the journal file. This is the
//     load-bearing limb: a committed op is FINISHED, never merely cleaned up.
//   - Complete — the op already reached done (its journal merely survived a crash
//     before self-cleanup): remove the stale file. The Finisher is NEVER called;
//     re-acting a finished op could double-apply its effect.
//   - Abandoned — the op only reached intent (crashed before the commit point):
//     remove the file. The Finisher is NEVER called; recovery does not enact a
//     never-committed op (roll FORWARD, never back) — any partial artifacts are
//     reconciled by the evidence-positive heals (isolate-repair / gc).
//
// The Finisher is INJECTED by the caller (the CLI/isolate/land layer) so journal
// stays free of higher-level dependencies and import cycles. It must be OFFLINE
// (no network) and IDEMPOTENT — recovery may re-run after a crash. A Finisher
// error is NON-FATAL: that op's journal is left in place so the next offline
// startup retries it, the failure is recorded in the report, and the pass
// continues. Recover does the journal-file I/O only and dials no network.
//
// PRECONDITION (the caller's responsibility, enforced by the startup hook in a
// later sub-unit): the worklist must contain only ops that are NOT concurrently
// in flight. Recovery runs offline at startup; the hook ensures an in-flight op
// (one a live process is still journaling) is not stomped — e.g. by running under
// the workspace lock or skipping ops whose task lock is held by a live PID.
package journal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Finisher completes the durable effect of a rolled-forward op. It is injected so
// journal stays decoupled from isolate/land. It must be offline and idempotent; a
// non-nil error leaves the op's journal in place for the next startup to retry.
type Finisher func(op OpRecovery) error

// RecoveryReport summarizes one offline recovery pass for the startup envelope.
// Each field lists the op_ids that took that path; all are nil when nothing was
// recovered.
type RecoveryReport struct {
	RolledForward []string // committed ops finished (Finisher ran, `done` journaled, file removed)
	Completed     []string // already-done ops whose stale journals were removed
	Abandoned     []string // never-committed ops whose journals were removed (partials left to heals)
	Failed        []string // ops whose Finisher errored — journal left in place, retried next startup
}

// Recover runs one offline roll-forward recovery pass over journalDir: it scans
// the subtree (Scan) and enacts each op's Disposition with finish as the
// kind-specific completion for rolled-forward ops. It returns a report of what
// took each path. A Finisher failure is non-fatal (recorded in report.Failed, the
// op's journal left for retry); Recover returns a non-nil error only on an
// unrecoverable scan or filesystem fault.
func Recover(journalDir string, finish Finisher) (RecoveryReport, error) {
	worklist, err := Scan(journalDir)
	if err != nil {
		return RecoveryReport{}, err
	}
	var rep RecoveryReport
	for _, op := range worklist {
		switch op.Disposition {
		case DispositionRollForward:
			if err := finish(op); err != nil {
				rep.Failed = append(rep.Failed, op.OpID)
				continue // leave the journal in place; retry next offline startup
			}
			if err := Append(journalDir, doneEntry(op)); err != nil {
				return rep, err
			}
			if err := removeOp(journalDir, op.OpID); err != nil {
				return rep, err
			}
			rep.RolledForward = append(rep.RolledForward, op.OpID)
		case DispositionComplete:
			if err := removeOp(journalDir, op.OpID); err != nil {
				return rep, err
			}
			rep.Completed = append(rep.Completed, op.OpID)
		case DispositionAbandoned:
			if err := removeOp(journalDir, op.OpID); err != nil {
				return rep, err
			}
			rep.Abandoned = append(rep.Abandoned, op.OpID)
		}
	}
	return rep, nil
}

// doneEntry is the terminal entry recovery appends to honestly close a rolled-
// forward op's lifecycle before removing its journal: same identity, phase done.
func doneEntry(op OpRecovery) Entry {
	return Entry{OpID: op.OpID, Kind: op.Kind, Phase: PhaseDone, Task: op.Task, Repos: op.Repos}
}

// removeOp deletes one op's journal file. An already-absent file is not an error
// (idempotent — recovery may re-run), so a crash between append-done and remove
// is harmless: the next pass sees done → Complete → removes it.
func removeOp(journalDir, opID string) error {
	path, err := opPath(journalDir, opID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("journal: remove %s: %w", path, err)
	}
	return nil
}
