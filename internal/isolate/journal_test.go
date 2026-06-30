package isolate_test

import (
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Guard HEAL-CRASH-RECOVER (write-side limb) — the integration that gives the
// offline roll-forward executor (sub-units 3a–3c) something real to recover. By
// decision #4 (RESOLVED 2026-06-30: roll-FORWARD), an interrupted `wi isolate rm`
// must finish its deletion on the next offline startup. For recovery to act, the
// command must FIRST record its lifecycle in the durable op journal: isolate.Remove
// wraps the non-journaling teardown core with intent → committed → done.
//
// isolate-rm teardown is idempotent and resumable (reclamation is evidence-positive
// per-repo; re-running reclaims only what it can re-prove it owns), so the WHOLE
// invocation is past the point of no return: Remove writes `committed` immediately
// after `intent`. A crashed isolate-rm therefore always rolls FORWARD (re-running is
// a safe no-op when nothing was reclaimed), never abandons. On a clean RemoveComplete
// the journal is closed (`done`) and self-cleaned (journal.Discard); on a hard block
// (an unexplained-orphan repo, RemoveBlocked) the journal is LEFT at `committed` so
// the next offline startup rolls it forward — reclaiming any now-unblocked repos.
//
// Non-vacuity (registered as the HEAL-CRASH-RECOVER write-side pair):
//   - (primary — commit point) skip the `committed` append (write only `intent`) →
//     a blocked op's journal reaches only intent → Classify = Abandoned, not
//     RollForward → TestRemoveLeavesCommittedJournalWhenBlocked RED.
//   - (alternate — self-clean) skip journal.Discard on RemoveComplete (still write
//     `done`) → a cleanly-completed op leaves a stale journal behind →
//     TestRemoveJournalsLifecycleAndSelfCleansOnComplete RED (worklist not empty).
//   - (alternate — never-started) drop the precondition-discard arm (fall through to
//     leave-at-`committed`) → a no-record rm leaves a committed journal recovery
//     retries forever → TestRemoveNoRecordLeavesNoJournal RED (worklist not empty).

// TestRemoveJournalsLifecycleAndSelfCleansOnComplete pins that a cleanly-completed
// isolate-rm leaves NO journal: it reached `done` and self-cleaned, so the offline
// recovery worklist is empty (nothing to roll forward).
func TestRemoveJournalsLifecycleAndSelfCleansOnComplete(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api")
	cloneSSOT(t, env, l, g, ctx, "web")

	if _, err := isolate.New(ctx, l, g, "feat", "op_test_jnew_done", specs("api", "web")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	res, err := isolate.Remove(ctx, l, g, "feat", "op_test_rm_jdone", nil) // full clean teardown
	if err != nil {
		t.Fatalf("isolate.Remove: %v", err)
	}
	if res.Status != isolate.RemoveComplete {
		t.Fatalf("Status = %q, want %q", res.Status, isolate.RemoveComplete)
	}

	ops, err := journal.Scan(l.JournalDir())
	if err != nil {
		t.Fatalf("journal.Scan: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("recovery worklist = %+v, want empty (completed op must self-clean its journal)", ops)
	}
}

// TestRemoveLeavesCommittedJournalWhenBlocked pins that an isolate-rm halted by a
// hard block leaves its journal at `committed` carrying the op's identity, so the
// offline recovery worklist reports it as RollForward — recovery will finish the
// teardown when the orphan is resolved.
func TestRemoveLeavesCommittedJournalWhenBlocked(t *testing.T) {
	env, l, g, ctx := setup(t)
	cloneSSOT(t, env, l, g, ctx, "api")
	cloneSSOT(t, env, l, g, ctx, "web")

	if _, err := isolate.New(ctx, l, g, "feat", "op_test_jnew_blk", specs("api", "web")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	// Move "web" ahead of base (clean tree, HEAD past the creation marker) → the
	// ahead-of-base orphan: a HARD BLOCK that halts the teardown at RemoveBlocked.
	webWT, _ := l.Isolate("feat", "web")
	env.Git(t, webWT, "commit", "--allow-empty", "-m", "local work")

	const opID = "op_test_rm_jblk"
	res, err := isolate.Remove(ctx, l, g, "feat", opID, []string{"api", "web"})
	if err != nil {
		t.Fatalf("isolate.Remove: %v", err)
	}
	if res.Status != isolate.RemoveBlocked {
		t.Fatalf("Status = %q, want %q (web is ahead of base)", res.Status, isolate.RemoveBlocked)
	}

	ops, err := journal.Scan(l.JournalDir())
	if err != nil {
		t.Fatalf("journal.Scan: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("recovery worklist = %+v, want exactly the blocked op", ops)
	}
	op := ops[0]
	if op.OpID != opID {
		t.Errorf("op.OpID = %q, want %q", op.OpID, opID)
	}
	if op.Disposition != journal.DispositionRollForward {
		t.Errorf("op.Disposition = %q, want %q (committed-but-not-done rolls forward)", op.Disposition, journal.DispositionRollForward)
	}
	if op.Kind != journal.KindIsolateRm {
		t.Errorf("op.Kind = %q, want %q", op.Kind, journal.KindIsolateRm)
	}
	if op.Task != "feat" {
		t.Errorf("op.Task = %q, want %q", op.Task, "feat")
	}
	if len(op.Repos) != 2 || op.Repos[0] != "api" || op.Repos[1] != "web" {
		t.Errorf("op.Repos = %v, want [api web]", op.Repos)
	}
}

// TestRemoveNoRecordLeavesNoJournal pins that an isolate-rm that never starts (no
// such isolate) leaves NO journal: removeCore returns ErrNoRecord before any
// teardown, so the op never crossed its commit point and the recovery worklist must
// be empty. A lingering committed journal here would make recovery retry a no-op
// (re-erroring on the absent record) forever.
func TestRemoveNoRecordLeavesNoJournal(t *testing.T) {
	_, l, g, ctx := setup(t)

	_, err := isolate.Remove(ctx, l, g, "ghost", "op_test_rm_norec", nil)
	if !errors.Is(err, state.ErrNoRecord) {
		t.Fatalf("isolate.Remove(no isolate) err = %v, want state.ErrNoRecord", err)
	}

	ops, serr := journal.Scan(l.JournalDir())
	if serr != nil {
		t.Fatalf("journal.Scan: %v", serr)
	}
	if len(ops) != 0 {
		t.Errorf("recovery worklist = %+v, want empty (a never-started rm must drop its journal)", ops)
	}
}
