package lock

import (
	"errors"
	"testing"
)

// Guard LOCK-MUTEX (unit, cross-acquirer exclusion semantics) — DESIGN §6.1.
//
// Acquire is non-blocking: flock TryLock is authoritative, so a key already held
// by another acquirer yields a typed *HeldError (the CLI maps this to exit 6
// lock_held), never a block and never a double-grant. Acquisition is all-or-
// nothing: on partial failure Acquire releases the locks it already took, leaving
// no leak. Release frees everything so a later acquirer succeeds.
//
// Exclusion is provable in-process because each FileLock is its own open file
// description, and BSD flock contends across descriptions even within one process
// (the same property guard FLOCK-EXCLUDES relies on).
//
// Non-vacuity:
//   - treat TryLock's "false" (refused) as success → the overlapping Acquire
//     wrongly succeeds → TestAcquireRefusesOverlap RED.
//   - skip releasing already-held locks on partial failure → the leaked lock stays
//     held → TestAcquireReleasesOnPartialFailure RED.

func TestAcquireAndReleaseRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a, _ := Repo("a")

	h, err := Acquire(dir, a)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Re-acquire after a clean release must succeed.
	h2, err := Acquire(dir, a)
	if err != nil {
		t.Fatalf("re-Acquire after release: %v", err)
	}
	if err := h2.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	// Release is safe to call twice.
	if err := h2.Release(); err != nil {
		t.Fatalf("idempotent Release: %v", err)
	}
}

func TestAcquireRefusesOverlap(t *testing.T) {
	dir := t.TempDir()
	a, _ := Repo("a")
	reg := ProjectRegistry()

	h1, err := Acquire(dir, a, reg)
	if err != nil {
		t.Fatalf("h1 Acquire: %v", err)
	}

	// A second acquirer sharing "repo:a" must be refused with *HeldError naming
	// the contended key — not blocked, not granted.
	_, err = Acquire(dir, a)
	if err == nil {
		h1.Release()
		t.Fatal("overlapping Acquire = nil error, want *HeldError")
	}
	var he *HeldError
	if !errors.As(err, &he) {
		h1.Release()
		t.Fatalf("error = %v (%T), want *HeldError", err, err)
	}
	if he.Key.String() != "repo:a" {
		t.Errorf("HeldError.Key = %q, want repo:a", he.Key.String())
	}

	// h1 is unaffected; once it releases, the key becomes acquirable.
	if err := h1.Release(); err != nil {
		t.Fatalf("h1 Release: %v", err)
	}
	h3, err := Acquire(dir, a)
	if err != nil {
		t.Fatalf("re-acquire after h1 release: %v", err)
	}
	h3.Release()
}

func TestAcquireReleasesOnPartialFailure(t *testing.T) {
	dir := t.TempDir()
	a, _ := Repo("a")
	b, _ := Repo("b") // "repo:a" < "repo:b": the total order locks a first, b second.

	// Hold the later-sorted key b with an independent acquirer.
	hB, err := Acquire(dir, b)
	if err != nil {
		t.Fatalf("hold b: %v", err)
	}
	defer hB.Release()

	// Acquire {a,b}: a (first in order) is free and gets locked; b (second) is
	// held, so the whole acquire fails — and must roll back the lock on a.
	if _, err := Acquire(dir, a, b); err == nil {
		t.Fatal("Acquire{a,b} = nil error, want *HeldError on b")
	}

	// Prove a was rolled back: a fresh acquirer must be able to take it.
	hA, err := Acquire(dir, a)
	if err != nil {
		t.Fatalf("Acquire{a} after partial failure: %v — 'a' leaked (not rolled back)", err)
	}
	hA.Release()
}
