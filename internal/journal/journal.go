// Package journal is wi's durable op-journal core (HEAL-4, DESIGN §7.4): the
// append-only record of every multi-step mutating operation's lifecycle, so an
// operation interrupted by a crash can be recovered deterministically on the next
// startup — OFFLINE-ONLY, by rolling FORWARD to finish what was already committed,
// never by guessing or by undoing committed work.
//
// This file owns the pure record + codec — the stable foundation the rest of HEAL-4
// builds on, exactly as lock.Holder's codec underpins the auto-break policy and
// gc.Classify underpins Inspect/Collect. The append-safe JSONL writer (over the
// single atomic .wi/ writer, DESIGN §6.2) and the roll-forward recovery scan are
// separate units layered on top; this one does no I/O and dials no network.
//
// The lifecycle is a three-phase ratchet (DESIGN §7.4 `intent→committed→done`):
//   - intent    — wi is ABOUT TO perform a non-idempotent mutation; the entry records
//     what, so recovery knows an op was in flight.
//   - committed — the mutation has crossed its point of no return (durably applied to
//     the authoritative store); recovery for this op rolls FORWARD to
//     finish it, never back (IMPLEMENTATION_PLAN §7 decision #4: isolate-rm
//     and friends recover by finishing, not restoring).
//   - done      — the op completed cleanly; there is nothing to recover.
//
// Phase and Kind are INTERNAL durable-state vocabularies (like state.Stage and
// gc.Class), NOT the closed wire enums agents parse — internal/contract remains the
// sole owner of those. They are still closed sets here: a journal line carrying a
// phase or kind wi does not understand means a torn or incompatible-build journal,
// which recovery must treat conservatively (surface it, never silently mis-recover),
// so ParseEntry REFUSES an unknown value rather than returning a zero-value Entry —
// the same conservative posture lock.ParseHolder takes on an unreadable lock body.
package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Phase is the durable lifecycle phase of a journaled operation (DESIGN §7.4).
type Phase string

const (
	PhaseIntent    Phase = "intent"
	PhaseCommitted Phase = "committed"
	PhaseDone      Phase = "done"
)

// Valid reports whether p is one of the three closed lifecycle phases.
func (p Phase) Valid() bool {
	switch p {
	case PhaseIntent, PhaseCommitted, PhaseDone:
		return true
	default:
		return false
	}
}

// Kind is the operation a journal entry describes — the closed set of multi-step
// mutating ops whose interruption needs roll-forward recovery. Single-step ops that
// are already crash-atomic (e.g. a sync base-ref update-ref) do not journal. The set
// is internal and grows additively as recoverable verbs land; it is not a wire enum.
type Kind string

const (
	KindIsolateNew Kind = "isolate_new"
	KindIsolateRm  Kind = "isolate_rm"
	KindLand       Kind = "land"
)

// Valid reports whether k is one of the closed operation kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindIsolateNew, KindIsolateRm, KindLand:
		return true
	default:
		return false
	}
}

// Entry is one durable op-journal record: one line of the append-only JSONL journal.
// An operation emits several entries over its life (an intent line, a committed line,
// a done line), all sharing OpID; recovery groups a journal's lines by OpID and reads
// each op's furthest-reached Phase to decide whether to roll forward. OpID is the
// correlation id the op, its lock holder, and its envelope all carry (CTX-OPID), so a
// recovered op can be tied back to the lock it held and the work it touched. Task and
// Repos name the target the recovery acts on; they are op-specific and omitted when
// empty (the codec validates only the identity + closed enums — a producer validates
// its own target shape).
type Entry struct {
	OpID  string   `json:"op_id"`
	Kind  Kind     `json:"kind"`
	Phase Phase    `json:"phase"`
	Task  string   `json:"task,omitempty"`
	Repos []string `json:"repos,omitempty"`
}

// Marshal renders the entry as a single newline-terminated JSON line, the JSONL unit
// the append-safe journal writer appends verbatim. The encoding is stable
// (encoding/json emits struct fields in declaration order and compact, with no
// embedded newlines), so a journal line written by one wi build reads back identically
// in another — the durability contract a crash-recovery store depends on. Marshal
// refuses an entry that fails Validate, so a malformed record can never reach the
// durable journal in the first place.
func (e Entry) Marshal() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal entry: %w", err)
	}
	return append(b, '\n'), nil
}

// Validate checks the structural invariants every journal entry must hold: a non-empty
// OpID (without it the line cannot be correlated to an op for recovery) and a Kind and
// Phase drawn from their closed sets. Target fields (Task/Repos) are not checked here —
// they are op-specific and validated by the producer.
func (e Entry) Validate() error {
	if e.OpID == "" {
		return fmt.Errorf("journal: entry has empty op_id")
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("journal: entry has unknown kind %q", e.Kind)
	}
	if !e.Phase.Valid() {
		return fmt.Errorf("journal: entry has unknown phase %q", e.Phase)
	}
	return nil
}

// ParseEntry decodes one JSONL line produced by Marshal. Empty/blank input, malformed
// JSON, or a record failing Validate (no op_id, or an unknown kind/phase) is an error —
// never a zero-value Entry — because recovery must treat a journal line it cannot fully
// understand conservatively: an op it cannot classify is surfaced, never silently
// skipped or mis-recovered. This mirrors lock.ParseHolder's refusal to invent a holder
// from an unreadable body.
func ParseEntry(line []byte) (Entry, error) {
	if len(bytes.TrimSpace(line)) == 0 {
		return Entry{}, fmt.Errorf("journal: empty entry line")
	}
	var e Entry
	if err := json.Unmarshal(line, &e); err != nil {
		return Entry{}, fmt.Errorf("journal: parse entry: %w", err)
	}
	if err := e.Validate(); err != nil {
		return Entry{}, err
	}
	return e, nil
}
