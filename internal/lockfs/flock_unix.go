//go:build unix

package lockfs

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// FileLock is an advisory, whole-file lock (BSD flock(2)) over a lock file, used
// to serialize concurrent wi processes that touch the same .wi/ resource (DESIGN
// §6.1). It is advisory (only cooperating wi processes honor it) and bound to the
// open file description, so the kernel releases it automatically when the holder
// process exits — a crashed holder never wedges the lock permanently, which is
// what makes the §7.3 self-heal a refinement rather than a necessity.
//
// A FileLock is single-use per acquire/release cycle and is NOT safe for
// concurrent use by multiple goroutines; the higher-level internal/lock package
// orchestrates named keys on top of it.
type FileLock struct {
	path string
	f    *os.File // non-nil exactly while held
}

// NewFileLock returns an unlocked FileLock for path. The lock file's parent
// directory must already exist (layout.Bootstrap creates .wi/locks); the lock
// file itself is created on first acquire.
func NewFileLock(path string) *FileLock {
	return &FileLock{path: path}
}

// Path returns the lock file path.
func (l *FileLock) Path() string { return l.path }

// open creates/opens the backing lock file for an acquire. Callers must hold the
// invariant that l.f is nil.
func (l *FileLock) open() (*os.File, error) {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lockfs: open lock %s: %w", l.path, err)
	}
	return f, nil
}

// TryLock attempts to take the exclusive lock without blocking. It returns
// (true, nil) on success, (false, nil) if another holder currently has it, or
// (false, err) on an unexpected error or misuse (handle already holding).
func (l *FileLock) TryLock() (bool, error) {
	if l.f != nil {
		return false, fmt.Errorf("lockfs: %s already locked by this handle", l.path)
	}
	f, err := l.open()
	if err != nil {
		return false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, fmt.Errorf("lockfs: flock %s: %w", l.path, err)
	}
	l.f = f
	return true, nil
}

// Lock blocks until the exclusive lock is acquired. It returns an error on an
// unexpected failure or misuse (handle already holding).
func (l *FileLock) Lock() error {
	if l.f != nil {
		return fmt.Errorf("lockfs: %s already locked by this handle", l.path)
	}
	f, err := l.open()
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return fmt.Errorf("lockfs: flock %s: %w", l.path, err)
	}
	l.f = f
	return nil
}

// Unlock releases the lock and closes the backing descriptor. It is a safe no-op
// on a handle that is not currently holding.
func (l *FileLock) Unlock() error {
	if l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	// Closing the descriptor alone would release the flock, but unlock explicitly
	// first so the intent is clear and any error is surfaced distinctly.
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	closeErr := f.Close()
	if unlockErr != nil {
		return fmt.Errorf("lockfs: unlock %s: %w", l.path, unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("lockfs: close lock %s: %w", l.path, closeErr)
	}
	return nil
}
