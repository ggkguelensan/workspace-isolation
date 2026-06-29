// Package lockfs owns wi's two filesystem-durability primitives: the SINGLE
// atomic file writer every .wi/ state writer reuses (this file, DESIGN §6.2) and,
// in a sibling file, the advisory flock used to serialize concurrent wi
// processes. Centralizing the write recipe here means crash-safety is proven once
// — by HEAL-ATOMIC-WRITE — instead of re-implemented (and re-broken) per writer.
package lockfs

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ggkguelensan/workspace-isolation/internal/fault"
)

// tmpPrefix names the in-progress temp file. The leading dot keeps it out of
// casual listings, and the distinctive stem lets startup recovery recognize and
// sweep abandoned temps (a crash between CreateTemp and rename can leave one).
const tmpPrefix = ".wi-atomic-"

// FaultBeforeRename is the WI_FAULT id that aborts WriteFileAtomic AFTER the temp
// file is fully written but BEFORE the rename — the exact window a power loss
// would expose. It is the mutant half of HEAL-ATOMIC-WRITE: with it active a
// correct implementation must leave the target's prior content wholly intact.
const FaultBeforeRename = "lockfs.before_rename"

// WriteFileAtomic writes data to path so that any concurrent or subsequent reader
// observes either the complete previous content or the complete new content,
// never a torn or truncated mixture — even if the process dies mid-write. It does
// this with the create-temp-then-rename recipe (DESIGN §6.2): write a temp file
// in the SAME directory (so the rename stays within one filesystem and is thus
// atomic), fsync it, set its mode, then rename it over path and fsync the parent
// directory so the rename itself is durable.
//
// path's parent directory must already exist (layout.Bootstrap creates the .wi/
// tree); WriteFileAtomic does not create intermediate directories. On any failure
// the partially written temp file is removed and path is left untouched.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)

	f, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return fmt.Errorf("lockfs: create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	// Until the rename succeeds, the temp is garbage: remove it on every error
	// path. After a successful rename tmp is cleared so this is a no-op.
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()

	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		return fmt.Errorf("lockfs: write temp %s: %w", tmp, werr)
	}

	// The crash window: temp content exists on disk but the target has not yet
	// been atomically swapped. A correct caller survives a death here unscathed.
	if fault.Active(FaultBeforeRename) {
		_ = f.Close()
		return fmt.Errorf("lockfs: injected crash before rename (%s=%s)", fault.EnvVar, FaultBeforeRename)
	}

	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		return fmt.Errorf("lockfs: fsync temp %s: %w", tmp, serr)
	}
	// CreateTemp makes the file 0600; set the caller's intended mode explicitly
	// (Chmod is not subject to umask) before it becomes the live file.
	if cerr := f.Chmod(perm); cerr != nil {
		_ = f.Close()
		return fmt.Errorf("lockfs: chmod temp %s: %w", tmp, cerr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("lockfs: close temp %s: %w", tmp, cerr)
	}

	if rerr := os.Rename(tmp, path); rerr != nil {
		return fmt.Errorf("lockfs: rename %s -> %s: %w", tmp, path, rerr)
	}
	tmp = "" // committed: the temp no longer exists under its old name

	if derr := fsyncDir(dir); derr != nil {
		return fmt.Errorf("lockfs: fsync dir %s: %w", dir, derr)
	}
	return nil
}

// fsyncDir flushes a directory entry change (here, the rename) to stable storage.
// Without it the renamed file can survive a crash while the directory entry
// pointing at it does not. Directory fsync is supported on the unix filesystems
// wi targets.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if serr := d.Sync(); serr != nil {
		_ = d.Close()
		return serr
	}
	return d.Close()
}
