//go:build unix

package lockfs

import (
	"fmt"
	"os"
)

// WriteBody writes b as the entire content of the held lock file, replacing any
// previous body. It is how the lock layer stamps the holder record
// ({pid,host,boot_id,op_id}) into a lock once the flock is taken, so a later
// inspector can read who holds it (DESIGN §6 / §7.3). The write is done in place
// on the locked inode — NOT via a temp-and-rename — because renaming would point
// the path at a new inode and detach the advisory flock the holder still owns.
// That is safe here precisely because we hold the EXCLUSIVE flock: no cooperating
// wi process writes the file concurrently, and same-host inspectors see the
// written bytes through the page cache immediately. Truncate-then-write ensures a
// shorter body leaves no stale tail from a longer predecessor.
//
// It is misuse — and an error — to WriteBody on a handle that does not currently
// hold the lock: a body must never be written for a lock we do not own.
func (l *FileLock) WriteBody(b []byte) error {
	if l.f == nil {
		return fmt.Errorf("lockfs: WriteBody on unheld lock %s", l.path)
	}
	if err := l.f.Truncate(0); err != nil {
		return fmt.Errorf("lockfs: truncate lock body %s: %w", l.path, err)
	}
	if _, err := l.f.WriteAt(b, 0); err != nil {
		return fmt.Errorf("lockfs: write lock body %s: %w", l.path, err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("lockfs: sync lock body %s: %w", l.path, err)
	}
	return nil
}

// ReadBodyAt reads the full body of the lock file at path WITHOUT taking the
// flock. Advisory flock does not block reads, so this is exactly how a process
// that just lost a TryLock race inspects the holder it could not displace. A
// missing file is surfaced as a not-exist error (errors.Is(err, os.ErrNotExist)),
// which the caller distinguishes from a present-but-unparseable body. The bytes
// are returned raw; interpreting them (as a holder record) is the lock layer's
// job.
func ReadBodyAt(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("lockfs: read lock body %s: %w", path, err)
	}
	return b, nil
}
