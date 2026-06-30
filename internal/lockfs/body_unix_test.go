//go:build unix

package lockfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Guard FLOCK-BODY (M4): a held flock must be able to carry a holder body that a
// SEPARATE inspector can read back by path while the lock is still held — that is
// how lock self-heal learns who holds a contended lock (it failed to acquire, so
// it reads the body to decide staleness; DESIGN §6 / §7.3). flock is advisory, so
// the inspector reads the file content without taking the lock. The body must be
// written in place on the locked inode (not via rename, which would detach the
// flock from the path) and a shorter rewrite must not leave a stale tail.
//
// Non-vacuity mutant (registered): make WriteBody `return nil` without writing →
// ReadBodyAt sees an empty body → the round-trip assertion reddens. Alternate:
// drop the Truncate(0) in WriteBody → a shorter rewrite leaves the old tail → the
// shorter-overwrite assertion reddens. Both confirmed RED before green.
func TestFlockBodyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k.lock")

	l := NewFileLock(path)
	ok, err := l.TryLock()
	if err != nil || !ok {
		t.Fatalf("TryLock: ok=%v err=%v", ok, err)
	}
	defer func() { _ = l.Unlock() }()

	payload := []byte(`{"pid":4242,"host":"node-7","boot_id":"boot_id:abc","op_id":"op-1"}` + "\n")
	if err := l.WriteBody(payload); err != nil {
		t.Fatalf("WriteBody: %v", err)
	}

	// An inspector reads the body BY PATH while the lock is still held (advisory
	// flock does not block reads).
	got, err := ReadBodyAt(path)
	if err != nil {
		t.Fatalf("ReadBodyAt: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("body round-trip mismatch:\n got  %q\n want %q", got, payload)
	}

	// A shorter rewrite must fully replace the body — no stale tail from the
	// longer prior content.
	short := []byte("{}\n")
	if err := l.WriteBody(short); err != nil {
		t.Fatalf("WriteBody short: %v", err)
	}
	got, err = ReadBodyAt(path)
	if err != nil {
		t.Fatalf("ReadBodyAt after short: %v", err)
	}
	if string(got) != string(short) {
		t.Errorf("shorter rewrite left a stale tail:\n got  %q\n want %q", got, short)
	}
}

func TestWriteBodyRequiresHeld(t *testing.T) {
	dir := t.TempDir()
	l := NewFileLock(filepath.Join(dir, "unheld.lock"))
	// Never locked: writing a holder body without holding the lock is misuse and
	// must error rather than silently create a body for a lock we do not own.
	if err := l.WriteBody([]byte("x")); err == nil {
		t.Error("WriteBody on an unheld lock = nil error, want an error")
	}
}

func TestReadBodyAtMissing(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadBodyAt(filepath.Join(dir, "nope.lock")); err == nil {
		t.Error("ReadBodyAt(missing) = nil error, want an error")
	}
	// Sanity: a not-exist-class error (errors.Is unwraps the %w wrap), not a
	// masked nil.
	if _, err := ReadBodyAt(filepath.Join(dir, "nope.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadBodyAt(missing) error = %v, want a not-exist error", err)
	}
}
