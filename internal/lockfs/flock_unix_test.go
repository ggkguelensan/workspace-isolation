//go:build unix

package lockfs

import (
	"path/filepath"
	"testing"
	"time"
)

// FLOCK-EXCLUDES (unit, level: cross-process mutual exclusion) — DESIGN §6.1,
// §7.3. FileLock is the advisory whole-file lock that serializes concurrent wi
// processes touching the same .wi/ resource. The contract: at most one holder at
// a time; a second acquirer is refused (TryLock) or blocked (Lock) until release.
//
// BSD flock(2) associates the lock with the OPEN FILE DESCRIPTION, so two
// independent os.OpenFile handles on the same path contend even within one
// process — which is exactly what lets these guards prove exclusion without
// spawning a child.
//
// Non-vacuity (guard→mutant): take the lock with LOCK_SH (shared) instead of
// LOCK_EX → two holders coexist → the "second holder is refused" assertion of
// TestFlockExcludesSecondHolder fails, and the blocked goroutine in
// TestLockBlocksUntilReleased acquires immediately → both RED.

func TestFlockExcludesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.lock")

	l1 := NewFileLock(path)
	ok, err := l1.TryLock()
	if err != nil {
		t.Fatalf("l1.TryLock: %v", err)
	}
	if !ok {
		t.Fatal("l1.TryLock = false on a fresh lock, want true")
	}

	// A second, independent handle must be refused while l1 holds.
	l2 := NewFileLock(path)
	ok, err = l2.TryLock()
	if err != nil {
		t.Fatalf("l2.TryLock: %v", err)
	}
	if ok {
		t.Fatal("l2.TryLock = true while l1 holds the lock — exclusion broken")
	}

	// After release the lock is acquirable again.
	if err := l1.Unlock(); err != nil {
		t.Fatalf("l1.Unlock: %v", err)
	}
	ok, err = l2.TryLock()
	if err != nil {
		t.Fatalf("l2.TryLock after release: %v", err)
	}
	if !ok {
		t.Fatal("l2.TryLock = false after l1 released — lock not freed")
	}
	if err := l2.Unlock(); err != nil {
		t.Fatalf("l2.Unlock: %v", err)
	}
	// Unlock is idempotent / safe on an unlocked handle.
	if err := l2.Unlock(); err != nil {
		t.Fatalf("second l2.Unlock (idempotency): %v", err)
	}
}

func TestLockBlocksUntilReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.lock")

	l1 := NewFileLock(path)
	if err := l1.Lock(); err != nil {
		t.Fatalf("l1.Lock: %v", err)
	}

	acquired := make(chan struct{})
	l2 := NewFileLock(path)
	go func() {
		if err := l2.Lock(); err == nil {
			close(acquired)
		}
	}()

	// While l1 holds, the blocking Lock() must not complete.
	select {
	case <-acquired:
		t.Fatal("l2.Lock() acquired while l1 still held — exclusion broken")
	case <-time.After(50 * time.Millisecond):
	}

	if err := l1.Unlock(); err != nil {
		t.Fatalf("l1.Unlock: %v", err)
	}

	// Once released, the blocked acquirer must proceed.
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("l2.Lock() did not acquire within 2s after release")
	}
	if err := l2.Unlock(); err != nil {
		t.Fatalf("l2.Unlock: %v", err)
	}
}

func TestDoubleLockSameHandleErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.lock")
	l := NewFileLock(path)
	if _, err := l.TryLock(); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	// Re-locking an already-held handle is a usage error, not a silent re-grant.
	if _, err := l.TryLock(); err == nil {
		t.Fatal("second TryLock on a held handle = nil error, want usage error")
	}
	if err := l.Lock(); err == nil {
		t.Fatal("Lock on a held handle = nil error, want usage error")
	}
	if err := l.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}
