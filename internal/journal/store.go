// This file is the append-safe per-op JSONL store layered on the pure codec in
// journal.go (HEAL-4 sub-unit 2, DESIGN §7.4/§6.2). Each operation owns ONE journal
// file, <journalDir>/<op_id>.jsonl, and a phase transition APPENDS a line to it. The
// per-op-file model (decision #JOURNAL-PER-OP-FILE) is what makes journaling race-free
// without a dedicated lock: distinct ops touch distinct files, and a single op is
// driven through its phases sequentially by one process, so no two writers ever contend
// for one file. It also reuses the SINGLE atomic .wi/ writer (lockfs.WriteFileAtomic)
// rather than introducing a second durability primitive — an append is read-prior →
// concatenate → atomic whole-file replace, so a crash mid-append leaves the prior
// lifecycle wholly intact (the old file survives the rename), never a torn line.
//
// Recovery (a later sub-unit) enumerates this directory, reads each op's lifecycle with
// ReadOp, and rolls forward any committed-but-not-done op. This file does the I/O only;
// it dials no network.
package journal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
)

// journalExt is the per-op journal file suffix. JSONL: one Entry per line.
const journalExt = ".jsonl"

// journalPerm is the journal file mode — owner-writable, world-readable, matching the
// other .wi/ state files; the runtime subtree is gitignored as a whole (DESIGN §1).
const journalPerm = 0o644

// opPath returns the journal file path for opID under journalDir, validating op_id as a
// single safe path segment through the one traversal chokepoint (layout.ValidateSegment,
// the same gate lock keys pass) so a crafted op_id can never escape the journal subtree.
func opPath(journalDir, opID string) (string, error) {
	if err := layout.ValidateSegment("op_id", opID); err != nil {
		return "", err
	}
	return filepath.Join(journalDir, opID+journalExt), nil
}

// Append appends one entry to its operation's journal file, creating the file on the
// first entry. The entry is validated (via Marshal) before any I/O, so a malformed
// record never reaches the durable journal. The write is the prior file content with the
// new line concatenated, committed through lockfs.WriteFileAtomic: the append is atomic
// and crash-safe — a reader sees either the journal without this line or the journal with
// it, never a torn mixture. journalDir must already exist (layout.Bootstrap creates it).
func Append(journalDir string, e Entry) error {
	line, err := e.Marshal()
	if err != nil {
		return err
	}
	path, err := opPath(journalDir, e.OpID)
	if err != nil {
		return err
	}
	prior, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("journal: read %s: %w", path, err)
	}
	return lockfs.WriteFileAtomic(path, append(prior, line...), journalPerm)
}

// ReadOp reads every entry previously appended for opID, in append (chronological) order.
// An op that never journaled reads back as the empty slice with no error — the idempotent
// posture state.Load and lock.List take on an absent record, so recovery treats an unseen
// op as "nothing to recover" rather than a failure. A line that fails ParseEntry (a torn
// or incompatible-build journal) IS an error: recovery must surface a journal it cannot
// fully understand, never silently skip an op (the conservative posture of the codec).
func ReadOp(journalDir, opID string) ([]Entry, error) {
	path, err := opPath(journalDir, opID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // no journal for this op → nothing to recover
		}
		return nil, fmt.Errorf("journal: read %s: %w", path, err)
	}
	var entries []Entry
	for _, line := range bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		e, perr := ParseEntry(line)
		if perr != nil {
			return nil, fmt.Errorf("journal: %s: %w", path, perr)
		}
		entries = append(entries, e)
	}
	return entries, nil
}
