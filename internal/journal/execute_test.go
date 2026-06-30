package journal_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/journal"
)

// Guard HEAL-CRASH-RECOVER (executor limb) — the offline roll-forward executor of
// HEAL-4 (DESIGN §7.4, PLAN 76-77, decision #4). journal.Recover scans the journal
// subtree (sub-unit 3b) and enacts each op's Disposition (sub-unit 3a):
//   - RollForward → run the injected kind-specific Finisher (the op's durable
//     completion, e.g. isolate_rm finishes its deletion), append `done`, then
//     remove the journal file. THE load-bearing limb: a committed-but-not-done op
//     is FINISHED, never merely cleaned up.
//   - Complete    → remove the stale journal file (op already reached done; finisher
//     is NEVER called — re-acting a finished op could double-apply its effect).
//   - Abandoned   → remove the journal file (op never crossed the commit point; the
//     finisher is NEVER called — recovery does not enact a never-committed op; any
//     partial artifacts are left to the evidence-positive heals isolate-repair/gc).
// The Finisher is INJECTED so journal stays free of isolate/land deps; it must be
// offline + idempotent (recovery may re-run after a crash). A finisher error is
// NON-FATAL: that op's journal is LEFT in place (retried next offline startup) and
// recorded in report.Failed; the pass continues and Recover returns nil.
//
// Non-vacuity (registered): (primary — roll-forward enactment) skip calling the
// Finisher in the RollForward arm (just journal done + remove) → the op's durable
// effect never happens → TestRecoverRollsForwardCommittedOps RED (finisher not
// called). (alternate — do-no-harm) call the Finisher in the Abandoned arm → a
// never-committed op is enacted → TestRecoverRemovesCompletedAndAbandoned RED
// (finisher called). (alternate — retry-on-failure) on a Finisher error, remove
// the journal instead of leaving it → TestRecoverFinisherErrorLeavesJournal RED
// (journal gone, no retry possible).

func opFileExists(t *testing.T, dir, opID string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, opID+".jsonl"))
	if err == nil {
		return true
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	t.Fatalf("stat op %s journal: %v", opID, err)
	return false
}

func TestRecoverRollsForwardCommittedOps(t *testing.T) {
	dir := t.TempDir()
	mustAppend(t, dir, "op_fwd", journal.PhaseIntent, journal.PhaseCommitted)

	var called []journal.OpRecovery
	finish := func(op journal.OpRecovery) error {
		called = append(called, op)
		return nil
	}
	rep, err := journal.Recover(dir, finish)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(called) != 1 || called[0].OpID != "op_fwd" {
		t.Fatalf("finisher called with %+v, want exactly op_fwd", called)
	}
	if called[0].Kind != journal.KindIsolateNew || called[0].Task != "feat" {
		t.Errorf("finisher op identity = kind %q task %q, want isolate_new/feat", called[0].Kind, called[0].Task)
	}
	if opFileExists(t, dir, "op_fwd") {
		t.Errorf("op_fwd journal still exists after roll-forward, want removed")
	}
	if len(rep.RolledForward) != 1 || rep.RolledForward[0] != "op_fwd" {
		t.Errorf("report.RolledForward = %v, want [op_fwd]", rep.RolledForward)
	}
}

func TestRecoverRemovesCompletedAndAbandoned(t *testing.T) {
	dir := t.TempDir()
	mustAppend(t, dir, "op_done", journal.PhaseIntent, journal.PhaseCommitted, journal.PhaseDone)
	mustAppend(t, dir, "op_ab", journal.PhaseIntent)

	finish := func(op journal.OpRecovery) error {
		t.Errorf("finisher must NOT be called for %s (disposition is not roll-forward)", op.OpID)
		return nil
	}
	rep, err := journal.Recover(dir, finish)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if opFileExists(t, dir, "op_done") {
		t.Errorf("op_done (complete) journal still exists, want removed")
	}
	if opFileExists(t, dir, "op_ab") {
		t.Errorf("op_ab (abandoned) journal still exists, want removed")
	}
	if len(rep.Completed) != 1 || rep.Completed[0] != "op_done" {
		t.Errorf("report.Completed = %v, want [op_done]", rep.Completed)
	}
	if len(rep.Abandoned) != 1 || rep.Abandoned[0] != "op_ab" {
		t.Errorf("report.Abandoned = %v, want [op_ab]", rep.Abandoned)
	}
}

func TestRecoverFinisherErrorLeavesJournal(t *testing.T) {
	dir := t.TempDir()
	mustAppend(t, dir, "op_fwd", journal.PhaseIntent, journal.PhaseCommitted)

	finish := func(op journal.OpRecovery) error {
		return fmt.Errorf("boom finishing %s", op.OpID)
	}
	rep, err := journal.Recover(dir, finish)
	if err != nil {
		t.Fatalf("Recover returned error %v, want nil (a single finisher failure is non-fatal)", err)
	}
	if !opFileExists(t, dir, "op_fwd") {
		t.Errorf("op_fwd journal removed after finisher error, want left in place for retry")
	}
	if len(rep.Failed) != 1 || rep.Failed[0] != "op_fwd" {
		t.Errorf("report.Failed = %v, want [op_fwd]", rep.Failed)
	}
	if len(rep.RolledForward) != 0 {
		t.Errorf("report.RolledForward = %v, want empty (finish failed)", rep.RolledForward)
	}
}
