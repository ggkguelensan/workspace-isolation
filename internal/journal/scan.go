// This file is the journal directory scan of HEAL-4 (DESIGN §7.4, PLAN 76-77):
// the offline recovery entry point. Scan enumerates the .wi/journal subtree,
// reads each operation's lifecycle (ReadOp), and pairs the op's identity with
// the recovery Disposition its furthest-reached phase calls for (Classify),
// producing the worklist the roll-forward executor (a later sub-unit) drives.
// It does the read-side I/O only and dials no network.
//
// It composes the conservative posture of its parts: a missing journal subtree
// is "nothing to recover" (empty, no error — recovery runs at startup before
// any op has journaled), but a journal it cannot parse (a torn line) or classify
// (a contentless file) is a HARD error — recovery must surface an op it cannot
// understand rather than silently drop a possibly-committed op from roll-forward.
package journal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// OpRecovery pairs one journaled operation's identity (op_id, and the kind/task/
// repos carried by its intent entry) with the recovery Disposition Classify
// derived from its lifecycle. It is one item of the offline recovery worklist.
type OpRecovery struct {
	OpID        string
	Kind        Kind
	Task        string
	Repos       []string
	Disposition Disposition
}

// Scan returns the offline recovery worklist for journalDir — one OpRecovery per
// <op_id>.jsonl file, in op_id order (os.ReadDir sorts by name, so the worklist
// is deterministic). A missing journalDir reads back as the empty worklist with
// no error (idempotent: no journal subtree yet means nothing to recover). A
// journal that ReadOp cannot parse (a torn line) or Classify rejects (a
// contentless file) is returned as an error, never silently skipped. Non-journal
// sidecars (anything not ending in .jsonl) and subdirectories are ignored.
func Scan(journalDir string) ([]OpRecovery, error) {
	dirEntries, err := os.ReadDir(journalDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // no journal subtree → nothing to recover
		}
		return nil, fmt.Errorf("journal: scan %s: %w", journalDir, err)
	}
	var ops []OpRecovery
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), journalExt) {
			continue
		}
		opID := strings.TrimSuffix(de.Name(), journalExt)
		entries, err := ReadOp(journalDir, opID)
		if err != nil {
			return nil, err // ReadOp already wraps with the file path
		}
		disp, err := Classify(entries)
		if err != nil {
			return nil, fmt.Errorf("journal: scan %s: %w", de.Name(), err)
		}
		// Classify rejects an empty set, so entries[0] is safe here: it is the
		// first-written (intent) line — the op's identity record.
		id := entries[0]
		ops = append(ops, OpRecovery{
			OpID:        opID,
			Kind:        id.Kind,
			Task:        id.Task,
			Repos:       id.Repos,
			Disposition: disp,
		})
	}
	return ops, nil
}
