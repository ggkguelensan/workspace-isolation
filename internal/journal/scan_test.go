package journal_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/journal"
)

// Guard HEAL-CRASH-RECOVER (scan limb) — the journal directory scan of HEAL-4
// (DESIGN §7.4, PLAN 76-77), the offline recovery entry point. Scan enumerates
// the .wi/journal/*.jsonl subtree, reads each op's lifecycle (ReadOp), and pairs
// the op's identity (op_id/kind/task/repos, from its intent entry) with the
// recovery Disposition its FURTHEST phase calls for (Classify). It produces the
// worklist the offline roll-forward executor (sub-unit 3c) then drives. Read-side
// I/O only; dials no network. It inherits the conservative posture of its parts:
// a missing journal subtree is "nothing to recover" (empty, no error), but a
// journal it cannot parse (torn line) or classify (contentless file) is a HARD
// error — recovery must surface an op it cannot understand, never silently skip it.
//
// Non-vacuity (registered): (primary — pairing) pair every op with
// DispositionComplete instead of its per-op Classify result → a crashed
// (committed/intent-only) op is reported as finished and dropped from the
// roll-forward worklist → TestScanPairsOpsWithDispositions RED on the
// roll-forward/abandoned ops. (alternate — error-surfacing) skip (`continue`)
// on a ReadOp/Classify error instead of returning it → a torn journal is
// silently dropped → TestScanTornJournalErrors RED (Scan returns no error).

// mustAppend journals opID through the given phases (full Entry each, via the
// store_test entry() helper — Kind isolate_new, Task "feat", Repos ["api"]).
func mustAppend(t *testing.T, dir, opID string, phases ...journal.Phase) {
	t.Helper()
	for _, p := range phases {
		if err := journal.Append(dir, entry(opID, p)); err != nil {
			t.Fatalf("Append %s/%s: %v", opID, p, err)
		}
	}
}

func TestScanPairsOpsWithDispositions(t *testing.T) {
	dir := t.TempDir()
	mustAppend(t, dir, "op_done", journal.PhaseIntent, journal.PhaseCommitted, journal.PhaseDone)
	mustAppend(t, dir, "op_fwd", journal.PhaseIntent, journal.PhaseCommitted)
	mustAppend(t, dir, "op_ab", journal.PhaseIntent)

	ops, err := journal.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := map[string]journal.Disposition{
		"op_done": journal.DispositionComplete,
		"op_fwd":  journal.DispositionRollForward,
		"op_ab":   journal.DispositionAbandoned,
	}
	if len(ops) != len(want) {
		t.Fatalf("Scan returned %d ops, want %d: %+v", len(ops), len(want), ops)
	}
	got := map[string]journal.Disposition{}
	for _, op := range ops {
		got[op.OpID] = op.Disposition
		// identity is carried from the intent entry (entry() helper).
		if op.Kind != journal.KindIsolateNew {
			t.Errorf("op %s: Kind = %q, want %q", op.OpID, op.Kind, journal.KindIsolateNew)
		}
		if op.Task != "feat" {
			t.Errorf("op %s: Task = %q, want %q", op.OpID, op.Task, "feat")
		}
		if len(op.Repos) != 1 || op.Repos[0] != "api" {
			t.Errorf("op %s: Repos = %v, want [api]", op.OpID, op.Repos)
		}
	}
	for opID, w := range want {
		if got[opID] != w {
			t.Errorf("Scan op %s: Disposition = %q, want %q", opID, got[opID], w)
		}
	}
}

// TestScanMissingDirIsEmpty pins the idempotent posture: a journal subtree that
// does not exist yet is "nothing to recover" — the empty worklist, no error
// (recovery runs on startup before any op has journaled).
func TestScanMissingDirIsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no-journal-here")
	ops, err := journal.Scan(dir)
	if err != nil {
		t.Fatalf("Scan(missing dir) = error %v, want nil", err)
	}
	if len(ops) != 0 {
		t.Errorf("Scan(missing dir) = %+v, want empty worklist", ops)
	}
}

// TestScanTornJournalErrors pins that a journal file Scan cannot parse is a HARD
// error, surfaced — never silently skipped. A skipped torn journal would hide a
// possibly-committed op from roll-forward recovery (a data-integrity bug).
func TestScanTornJournalErrors(t *testing.T) {
	dir := t.TempDir()
	mustAppend(t, dir, "op_ok", journal.PhaseIntent, journal.PhaseCommitted)
	torn := filepath.Join(dir, "op_torn.jsonl")
	if err := os.WriteFile(torn, []byte("{not valid json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Scan(dir); err == nil {
		t.Errorf("Scan over a torn journal = nil error, want an error (must surface, never silently skip)")
	}
}

// TestScanSkipsNonJournalFiles pins that Scan only considers *.jsonl files, so a
// stray sidecar (a temp file, an editor backup) in the subtree does not derail
// recovery or get misread as an op journal.
func TestScanSkipsNonJournalFiles(t *testing.T) {
	dir := t.TempDir()
	mustAppend(t, dir, "op_real", journal.PhaseIntent, journal.PhaseCommitted)
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("not a journal"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops, err := journal.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(ops) != 1 || ops[0].OpID != "op_real" {
		t.Errorf("Scan = %+v, want exactly the op_real journal", ops)
	}
}
