package recovery_test

import (
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/recovery"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Guard HEAL-CRASH-RECOVER (startup-pass limb) — recovery.Run, the OFFLINE roll-forward
// recovery pass the startup hook calls before any command body (HEAL-4 sub-unit 3d-iv,
// DESIGN §7.4). It composes the three pieces already built: it takes lock.Workspace()
// non-blocking, runs journal.Recover over l.JournalDir() with the dispatcher's Finisher
// injected, and reports the outcome. Two load-bearing properties:
//
//   - it actually FINISHES a committed-but-not-done op (the durable teardown happens and
//     the journal is reaped), and
//   - it DEFERS (Skipped, touching nothing) when another process already holds the
//     workspace lock — startup must never block on, nor double-run, a concurrent pass.
//
// Non-vacuity (registered as the HEAL-CRASH-RECOVER startup-pass pair):
//   - (primary — does-the-work) after taking the lock, `return Report{}, nil` without
//     calling journal.Recover → the crashed teardown never finishes →
//     TestRunRollsForwardCommittedIsolateRm RED (RolledForward empty AND the isolate
//     record still present — asserting the durable side-effect, not a bare return, the
//     LOCK-HOLDER lesson).
//   - (alternate — in-flight skip) drop the HeldError→Skipped branch (let the acquire
//     error propagate) → TestRunSkipsWhenWorkspaceLockHeld RED (err non-nil, Skipped
//     false).

// journalCommittedRm writes the intent+committed entries of a crashed-after-commit
// isolate_rm op for task (no `done`): the precondition Run must roll FORWARD.
func journalCommittedRm(t *testing.T, journalDir, opID, task string) {
	t.Helper()
	for _, ph := range []journal.Phase{journal.PhaseIntent, journal.PhaseCommitted} {
		e := journal.Entry{OpID: opID, Kind: journal.KindIsolateRm, Phase: ph, Task: task}
		if err := journal.Append(journalDir, e); err != nil {
			t.Fatalf("journal.Append(%s): %v", ph, err)
		}
	}
}

func TestRunRollsForwardCommittedIsolateRm(t *testing.T) {
	l, g, ctx := seedIsolate(t, "api", "web")
	const opID = "op_rm_crashed"
	journalCommittedRm(t, l.JournalDir(), opID, "feat")

	rep, err := recovery.Run(ctx, l, g)
	if err != nil {
		t.Fatalf("recovery.Run = %v, want nil", err)
	}
	if rep.Skipped {
		t.Fatalf("rep.Skipped = true, want false (the workspace lock was free)")
	}
	if got := rep.Journal.RolledForward; len(got) != 1 || got[0] != opID {
		t.Errorf("rep.Journal.RolledForward = %v, want [%q]", got, opID)
	}
	// The durable effect: the interrupted teardown is now complete.
	if _, err := state.Load(l.StateDir(), "feat"); !errors.Is(err, state.ErrNoRecord) {
		t.Errorf("state.Load after Run err = %v, want ErrNoRecord (the teardown must finish)", err)
	}
	// The lifecycle is honestly closed: the journal was reaped.
	ops, err := journal.Scan(l.JournalDir())
	if err != nil {
		t.Fatalf("journal.Scan: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("journal worklist after Run = %v, want empty (the rolled-forward journal must be discarded)", ops)
	}
}

func TestRunSkipsWhenWorkspaceLockHeld(t *testing.T) {
	l, g, ctx := seedIsolate(t, "api")
	const opID = "op_rm_inflight"
	journalCommittedRm(t, l.JournalDir(), opID, "feat")

	// Simulate another process mid-recovery: hold the workspace lock for the whole call.
	held, err := lock.Acquire(l.LocksDir(), lock.Workspace())
	if err != nil {
		t.Fatalf("pre-acquire workspace lock: %v", err)
	}
	defer held.Release()

	rep, err := recovery.Run(ctx, l, g)
	if err != nil {
		t.Fatalf("recovery.Run with workspace lock held = %v, want nil (a contended pass defers, it does not fail)", err)
	}
	if !rep.Skipped {
		t.Fatalf("rep.Skipped = false, want true (recovery must defer to the process holding the workspace lock)")
	}
	// Nothing was touched: the op was not rolled forward and the isolate still stands.
	if _, err := state.Load(l.StateDir(), "feat"); err != nil {
		t.Errorf("state.Load after a skipped pass err = %v, want nil (the isolate must be untouched)", err)
	}
	ops, err := journal.Scan(l.JournalDir())
	if err != nil {
		t.Fatalf("journal.Scan: %v", err)
	}
	if len(ops) != 1 {
		t.Errorf("journal worklist after a skipped pass = %v, want the 1 untouched op", ops)
	}
}
