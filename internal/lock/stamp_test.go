package lock

import (
	"errors"
	"os"
	"testing"
)

// Guard LOCK-STAMP (M4): once a lock is acquired, its holder identity must be
// recorded into the lock file's body so a contender that loses the TryLock race
// can read WHO holds it and reason about staleness (DESIGN §6 / §7.3). Held.Stamp
// writes the current holder into EVERY lock the Held owns; ReadHolder reads one
// key's recorded holder back by path (advisory flock does not block the read). A
// lock that was acquired but never stamped, or has no file at all, reads as an
// error — an unknown holder is treated conservatively (never broken), never as a
// usable zero-value Holder that could be mistaken for a real pid-0 holder.
//
// Non-vacuity (guard→mutant):
//   - make Stamp return nil without writing any body → ReadHolder sees an empty
//     body → TestStampRoundTrip RED.
//   - make Stamp write only h.locks[0] (skip the rest) → the second key's body
//     stays empty → TestStampStampsEveryHeldLock RED.
//
// Both confirmed RED before green.
func TestStampRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a, _ := Repo("a")

	h, err := Acquire(dir, a)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()

	const opID = "op_test_stamp_1"
	if err := h.Stamp(opID); err != nil {
		t.Fatalf("Stamp: %v", err)
	}

	// A contender reads the holder back by key while the lock is still held.
	got, err := ReadHolder(dir, a)
	if err != nil {
		t.Fatalf("ReadHolder: %v", err)
	}
	if got.PID != os.Getpid() {
		t.Errorf("holder PID = %d, want this process %d", got.PID, os.Getpid())
	}
	if got.OpID != opID {
		t.Errorf("holder OpID = %q, want %q", got.OpID, opID)
	}
	if got.Host == "" || got.BootID == "" {
		t.Errorf("holder host/boot = %q/%q, want both populated", got.Host, got.BootID)
	}
}

func TestStampStampsEveryHeldLock(t *testing.T) {
	dir := t.TempDir()
	a, _ := Repo("a")
	b, _ := Repo("b")

	h, err := Acquire(dir, a, b)
	if err != nil {
		t.Fatalf("Acquire{a,b}: %v", err)
	}
	defer func() { _ = h.Release() }()

	const opID = "op_test_stamp_2"
	if err := h.Stamp(opID); err != nil {
		t.Fatalf("Stamp: %v", err)
	}

	// BOTH keys must carry the holder — Stamp records identity into every lock the
	// operation holds, not just the first.
	for _, k := range []Key{a, b} {
		got, err := ReadHolder(dir, k)
		if err != nil {
			t.Fatalf("ReadHolder(%s): %v", k.String(), err)
		}
		if got.OpID != opID {
			t.Errorf("ReadHolder(%s).OpID = %q, want %q", k.String(), got.OpID, opID)
		}
	}
}

func TestReadHolderUnstampedOrMissing(t *testing.T) {
	dir := t.TempDir()
	a, _ := Repo("a")

	// No lock file at all: ReadHolder surfaces a not-exist-class error (errors.Is
	// unwraps the %w chain), not a masked nil.
	if _, err := ReadHolder(dir, a); err == nil {
		t.Error("ReadHolder(missing) = nil error, want an error")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadHolder(missing) err = %v, want a not-exist error", err)
	}

	// Acquired but never stamped: flock created the file, but it has no body. An
	// unstamped lock must read as an error (unknown holder, never breakable), not a
	// zero-value Holder.
	h, err := Acquire(dir, a)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()
	if _, err := ReadHolder(dir, a); err == nil {
		t.Error("ReadHolder(unstamped) = nil error, want an error on empty body")
	}
}
