package journal_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/fault"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
)

// Guard JOURNAL-STORE — the append-safe per-op JSONL store (HEAL-4 sub-unit 2,
// DESIGN §7.4/§6.2). Each operation owns one journal file <journalDir>/<op_id>.jsonl;
// a phase transition APPENDS a line (it never rewrites history), the file is written
// through the single atomic .wi/ writer (lockfs.WriteFileAtomic) so an append is
// crash-safe, and ReadOp recovers an op's lifecycle in append order. The three
// fitnesses pin the three properties recovery depends on:
//
//   - append accumulates: a second Append does not clobber the first, so the full
//     intent→committed→done lifecycle survives for recovery to read;
//   - per-op isolation: an op reads only its own lines, so one op's journal can never
//     contaminate another's recovery verdict (this is what makes the per-op-file model
//     race-free — distinct ops touch distinct files);
//   - crash-safe append: an append interrupted mid-write (the WriteFileAtomic crash
//     window) leaves the prior lifecycle wholly intact and the new line absent — never
//     a torn or truncated journal.
//
// Non-vacuity (registered): (primary — append-safety) make Append write only the new
// line instead of prior+new → TestAppendAccumulatesLifecycle RED (ReadOp sees only the
// last entry). (alternate — per-op isolation) make the file path ignore op_id (a
// constant filename) → TestReadOpIsolatesByOpID RED (one op reads the other's lines).
// (alternate — atomic-writer crash-safety) make Append use os.WriteFile instead of
// lockfs.WriteFileAtomic → the WI_FAULT crash seam is bypassed, the faulted Append
// SUCCEEDS → TestAppendCrashLeavesPriorIntact RED (the interrupted line is present and
// no error is returned).

func entry(opID string, p journal.Phase) journal.Entry {
	return journal.Entry{OpID: opID, Kind: journal.KindIsolateNew, Phase: p, Task: "feat", Repos: []string{"api"}}
}

func TestAppendAccumulatesLifecycle(t *testing.T) {
	dir := t.TempDir()
	op := "op_acc_1"
	for _, p := range []journal.Phase{journal.PhaseIntent, journal.PhaseCommitted, journal.PhaseDone} {
		if err := journal.Append(dir, entry(op, p)); err != nil {
			t.Fatalf("Append %s: %v", p, err)
		}
	}

	got, err := journal.ReadOp(dir, op)
	if err != nil {
		t.Fatalf("ReadOp: %v", err)
	}
	want := []journal.Phase{journal.PhaseIntent, journal.PhaseCommitted, journal.PhaseDone}
	if len(got) != len(want) {
		t.Fatalf("ReadOp returned %d entries, want %d (append must accumulate, not overwrite): %+v", len(got), len(want), got)
	}
	for i, e := range got {
		if e.Phase != want[i] {
			t.Errorf("entry %d phase = %q, want %q (append order must be chronological)", i, e.Phase, want[i])
		}
		if e.OpID != op {
			t.Errorf("entry %d op_id = %q, want %q", i, e.OpID, op)
		}
	}
}

func TestReadOpIsolatesByOpID(t *testing.T) {
	dir := t.TempDir()
	if err := journal.Append(dir, entry("op_a", journal.PhaseIntent)); err != nil {
		t.Fatalf("Append op_a: %v", err)
	}
	if err := journal.Append(dir, entry("op_b", journal.PhaseCommitted)); err != nil {
		t.Fatalf("Append op_b: %v", err)
	}

	a, err := journal.ReadOp(dir, "op_a")
	if err != nil {
		t.Fatalf("ReadOp op_a: %v", err)
	}
	if len(a) != 1 || a[0].OpID != "op_a" || a[0].Phase != journal.PhaseIntent {
		t.Errorf("ReadOp(op_a) = %+v, want exactly op_a's single intent line (no contamination from op_b)", a)
	}

	b, err := journal.ReadOp(dir, "op_b")
	if err != nil {
		t.Fatalf("ReadOp op_b: %v", err)
	}
	if len(b) != 1 || b[0].OpID != "op_b" || b[0].Phase != journal.PhaseCommitted {
		t.Errorf("ReadOp(op_b) = %+v, want exactly op_b's single committed line", b)
	}

	// An op that never journaled reads back empty, never an error — the idempotent
	// posture state.Load/lock.List take on an absent record.
	none, err := journal.ReadOp(dir, "op_never")
	if err != nil {
		t.Fatalf("ReadOp(op_never): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ReadOp(op_never) = %+v, want empty (no journal for an unseen op)", none)
	}
}

func TestAppendCrashLeavesPriorIntact(t *testing.T) {
	dir := t.TempDir()
	op := "op_crash_1"
	if err := journal.Append(dir, entry(op, journal.PhaseIntent)); err != nil {
		t.Fatalf("Append intent: %v", err)
	}
	if err := journal.Append(dir, entry(op, journal.PhaseCommitted)); err != nil {
		t.Fatalf("Append committed: %v", err)
	}

	// Inject the WriteFileAtomic crash window on the third append: it must fail, and
	// because the journal is written through the single atomic writer, the prior two
	// lines survive byte-for-byte and the interrupted line is absent.
	t.Setenv(fault.EnvVar, lockfs.FaultBeforeRename)
	if err := journal.Append(dir, entry(op, journal.PhaseDone)); err == nil {
		t.Fatalf("Append done under injected crash returned nil, want an error")
	}

	got, err := journal.ReadOp(dir, op)
	if err != nil {
		t.Fatalf("ReadOp after crash: %v", err)
	}
	want := []journal.Phase{journal.PhaseIntent, journal.PhaseCommitted}
	if len(got) != len(want) {
		t.Fatalf("ReadOp after crash returned %d entries, want %d (prior lifecycle must survive, done line must be absent): %+v", len(got), len(want), got)
	}
	for i, e := range got {
		if e.Phase != want[i] {
			t.Errorf("entry %d phase = %q, want %q", i, e.Phase, want[i])
		}
	}
}
