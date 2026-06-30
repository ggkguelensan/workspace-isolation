package journal_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/journal"
)

// Guard JOURNAL-CODEC — the pure record + codec of wi's durable op journal (HEAL-4,
// DESIGN §7.4). The journal is the crash-recovery store: a line written by one wi
// build must read back byte-identically in another, and a line wi cannot fully
// understand must be REFUSED rather than silently mis-recovered. This fitness pins
// three things over the codec:
//
//   - round-trip identity (Marshal → ParseEntry recovers the same Entry);
//   - the concrete DURABLE WIRE KEYS (op_id/kind/phase/task/repos) and the JSONL shape
//     (exactly one trailing newline, no embedded newlines) — NOT just round-trip,
//     because a round-trip-only assertion is vacuous against a json-tag rename
//     (Marshal and Unmarshal share the tag, so they stay symmetric while the durable
//     key silently changes — the methodology trap caught on the lock.Holder unit);
//   - conservative closed-enum + identity validation: an unknown phase/kind or a
//     missing op_id is an error, never a zero-value Entry.
//
// Non-vacuity (registered): (primary — closed-enum guard) make journal.Phase.Valid
// return true unconditionally → ParseEntry of a line with phase "halfway" succeeds →
// TestRejectsUnknownPhaseKindAndMissingOpID RED. (alternate — wire-key stability)
// rename the `phase` json tag to `state` → the concrete-key assertion in
// TestMarshalRoundTripAndWireKeys RED while the round-trip assertion stays GREEN,
// proving the wire-key check is the load-bearing one.
func TestMarshalRoundTripAndWireKeys(t *testing.T) {
	e := journal.Entry{
		OpID:  "op_abc_def",
		Kind:  journal.KindIsolateNew,
		Phase: journal.PhaseCommitted,
		Task:  "feat",
		Repos: []string{"api", "web"},
	}

	line, err := e.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// JSONL shape: exactly one newline, and it terminates the line.
	if got := bytes.Count(line, []byte{'\n'}); got != 1 {
		t.Errorf("marshaled entry has %d newlines, want exactly 1 (JSONL line)", got)
	}
	if !bytes.HasSuffix(line, []byte{'\n'}) {
		t.Errorf("marshaled entry must be newline-terminated, got %q", line)
	}

	// Concrete durable wire keys — a journal a future build must still read.
	for _, key := range []string{`"op_id"`, `"kind"`, `"phase"`, `"task"`, `"repos"`} {
		if !bytes.Contains(line, []byte(key)) {
			t.Errorf("marshaled entry %q missing durable wire key %s", line, key)
		}
	}
	// And the closed-enum VALUES are persisted verbatim (not re-encoded).
	for _, val := range []string{`"isolate_new"`, `"committed"`} {
		if !bytes.Contains(line, []byte(val)) {
			t.Errorf("marshaled entry %q missing durable value %s", line, val)
		}
	}

	got, err := journal.ParseEntry(line)
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	if !reflect.DeepEqual(got, e) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, e)
	}
}

// TestRejectsUnknownPhaseKindAndMissingOpID pins the conservative posture: ParseEntry
// refuses any line it cannot fully classify — an unknown lifecycle phase, an unknown op
// kind, or a record with no op_id to correlate against — returning an error, never a
// silently-degraded zero Entry that recovery might act on.
func TestRejectsUnknownPhaseKindAndMissingOpID(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"unknown phase", `{"op_id":"op_x","kind":"land","phase":"halfway"}`},
		{"unknown kind", `{"op_id":"op_x","kind":"frobnicate","phase":"intent"}`},
		{"missing op_id", `{"kind":"land","phase":"intent"}`},
		{"empty line", ``},
		{"blank line", `   `},
		{"malformed json", `{"op_id":"op_x",`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := journal.ParseEntry([]byte(tc.line)); err == nil {
				t.Errorf("ParseEntry(%q) = (%+v, nil), want an error", tc.line, got)
			}
		})
	}
}

// TestPhaseAndKindValidMembership pins the closed sets directly: exactly the three
// phases and the three kinds are valid, and a stray value is not.
func TestPhaseAndKindValidMembership(t *testing.T) {
	for _, p := range []journal.Phase{journal.PhaseIntent, journal.PhaseCommitted, journal.PhaseDone} {
		if !p.Valid() {
			t.Errorf("Phase %q must be valid", p)
		}
	}
	if journal.Phase("parked").Valid() {
		t.Errorf("unknown phase %q must be invalid", "parked")
	}
	for _, k := range []journal.Kind{journal.KindIsolateNew, journal.KindIsolateRm, journal.KindLand} {
		if !k.Valid() {
			t.Errorf("Kind %q must be valid", k)
		}
	}
	if journal.Kind("sync").Valid() {
		t.Errorf("unknown kind %q must be invalid", "sync")
	}
}
