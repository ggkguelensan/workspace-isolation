package isolate_test

import (
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Guard HEAL-CRASH-RECOVER (finisher limb) — the domain-side recovery completion the
// offline roll-forward executor (sub-unit 3c) injects for a rolled-forward isolate-rm
// op (HEAL-4 sub-unit 3d-ii, decision #4: roll FORWARD). When `wi isolate rm` is
// interrupted after its commit point, the journal is left at `committed`; on the next
// offline startup the executor classifies it RollForward and calls FinishRemove to
// finish the teardown. FinishRemove re-runs the NON-journaling teardown core for the
// op's recorded task/repos and journals nothing — the executor owns every journal
// mutation during recovery (DESIGN §7.4).
//
// Its return value answers the executor's only question — "did the durable effect
// complete?" — so the executor knows whether to close + discard the journal or leave
// it for retry:
//   - RemoveComplete             → nil  (teardown finished; executor writes `done`, discards)
//   - already gone (ErrNoRecord) → nil  (teardown completed before the crash — idempotent)
//   - RemoveBlocked              → error (orphan persists; journal LEFT for next startup)
//   - any other fault            → error (left for retry)
//
// Non-vacuity (registered as the HEAL-CRASH-RECOVER finisher-limb pair):
//   - (primary — leave-for-retry) treat RemoveBlocked as success (return nil) → a
//     still-blocked orphan's journal would be discarded, losing the retry →
//     TestFinishRemoveStillBlockedLeavesForRetry RED (wants a non-nil error).
//   - (alternate — idempotency) drop the ErrNoRecord→nil case (let it fall through to
//     an error) → re-running a completed teardown errors forever →
//     TestFinishRemoveOnAlreadyRemovedIsIdempotent RED.
//   - (alternate — actually-does-the-work) make FinishRemove a no-op (return nil
//     without calling removeCore) → the interrupted teardown never finishes →
//     TestFinishRemoveCompletesInterruptedTeardown RED (record still present).

// TestFinishRemoveCompletesInterruptedTeardown pins the load-bearing happy path: an
// isolate that was materialized but whose rm never finished (state record + worktrees
// still present, mimicking a crash after `committed`) is fully torn down by
// FinishRemove — the record is gone afterward — and FinishRemove writes NO journal.
func TestFinishRemoveCompletesInterruptedTeardown(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api")
	cloneSSOT(t, env, l, g, ctx, "web")

	if _, err := isolate.New(ctx, l, g, "feat", "op_new", specs("api", "web")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	// The op the executor would hand us for a committed-but-not-done isolate-rm.
	op := journal.OpRecovery{OpID: "op_rm_recover", Kind: journal.KindIsolateRm, Task: "feat", Repos: nil}
	if err := isolate.FinishRemove(ctx, l, g, op); err != nil {
		t.Fatalf("FinishRemove: %v", err)
	}

	if _, err := state.Load(l.StateDir(), "feat"); !errors.Is(err, state.ErrNoRecord) {
		t.Errorf("state.Load after FinishRemove err = %v, want ErrNoRecord (teardown must complete)", err)
	}
	// FinishRemove must journal nothing — the executor owns recovery journal mutations.
	ops, err := journal.Scan(l.JournalDir())
	if err != nil {
		t.Fatalf("journal.Scan: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("recovery worklist = %+v, want empty (FinishRemove must not journal)", ops)
	}
}

// TestFinishRemoveOnAlreadyRemovedIsIdempotent pins that re-running the finisher after
// the teardown already completed (the record is gone) is a no-op SUCCESS, not an
// error — recovery may re-run after a crash between finishing the work and discarding
// the journal, and an error there would wedge the op in perpetual retry.
func TestFinishRemoveOnAlreadyRemovedIsIdempotent(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api")

	if _, err := isolate.New(ctx, l, g, "feat", "op_new", specs("api")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	if res, err := isolate.Remove(ctx, l, g, "feat", "op_rm1", nil); err != nil || res.Status != isolate.RemoveComplete {
		t.Fatalf("seed Remove: status %q err %v", res.Status, err)
	}

	op := journal.OpRecovery{OpID: "op_rm_recover", Kind: journal.KindIsolateRm, Task: "feat", Repos: nil}
	if err := isolate.FinishRemove(ctx, l, g, op); err != nil {
		t.Errorf("FinishRemove on already-removed isolate err = %v, want nil (idempotent)", err)
	}
}

// TestFinishRemoveStillBlockedLeavesForRetry pins the primary limb: when the teardown
// is still hard-blocked (an ahead-of-base orphan), FinishRemove returns an error so
// the executor LEAVES the journal in place — the next offline startup retries and can
// reclaim any now-unblocked repos. A nil here would discard the journal and abandon
// the still-undone teardown.
func TestFinishRemoveStillBlockedLeavesForRetry(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api")
	cloneSSOT(t, env, l, g, ctx, "web")

	if _, err := isolate.New(ctx, l, g, "feat", "op_new", specs("api", "web")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	// Move "web" ahead of base → the ahead-of-base orphan, a hard block.
	webWT, _ := l.Isolate("feat", "web")
	env.Git(t, webWT, "commit", "--allow-empty", "-m", "local work")

	op := journal.OpRecovery{OpID: "op_rm_recover", Kind: journal.KindIsolateRm, Task: "feat", Repos: []string{"api", "web"}}
	if err := isolate.FinishRemove(ctx, l, g, op); err == nil {
		t.Errorf("FinishRemove on a still-blocked orphan err = nil, want non-nil (journal must be left for retry)")
	}
}
