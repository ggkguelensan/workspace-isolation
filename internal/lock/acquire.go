package lock

import (
	"errors"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
)

// HeldError reports that a key could not be acquired because another process
// currently holds it. The CLI maps this to exit 6 lock_held (DESIGN §6.1).
type HeldError struct {
	Key Key
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("lock: %q is held by another process", e.Key.String())
}

// Held is a set of acquired locks recorded in acquisition order. Release frees
// them all; it is the only handle a caller keeps.
type Held struct {
	locks []*lockfs.FileLock
}

// Acquire takes every key with non-blocking flock TryLock, in orderedUnique order
// (sorted, deduped). It is all-or-nothing: if any key is already held it acquires
// NONE — rolling back whatever it already took — and returns *HeldError naming
// the contended key. locksDir must already exist (layout.Bootstrap creates
// .wi/locks). The returned *Held is nil exactly when err is non-nil.
func Acquire(locksDir string, keys ...Key) (*Held, error) {
	ordered := orderedUnique(keys)
	h := &Held{locks: make([]*lockfs.FileLock, 0, len(ordered))}
	for _, k := range ordered {
		fl := lockfs.NewFileLock(k.Path(locksDir))
		ok, err := fl.TryLock()
		if err != nil {
			_ = h.Release()
			return nil, fmt.Errorf("lock: acquire %q: %w", k.String(), err)
		}
		if !ok {
			_ = h.Release()
			return nil, &HeldError{Key: k}
		}
		h.locks = append(h.locks, fl)
	}
	return h, nil
}

// Release unlocks every held lock in reverse acquisition order and clears the
// set, so it is safe to call more than once. Errors from individual unlocks are
// joined.
func (h *Held) Release() error {
	var errs []error
	for i := len(h.locks) - 1; i >= 0; i-- {
		if err := h.locks[i].Unlock(); err != nil {
			errs = append(errs, err)
		}
	}
	h.locks = nil
	return errors.Join(errs...)
}
