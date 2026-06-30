package state_test

import (
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Guard STATE-KV-CAS (fitness, level: unit) — DESIGN §8.
//
// internal/state owns the namespaced compare-and-swap KV store backing the
// `wi state cas` agent-coordination primitive. Its load-bearing properties:
//   - COMPARE: a CAS writes the new value ONLY when the key's current value
//     equals `expected` (with AbsentSentinel meaning "expect the key absent").
//     A mismatch returns swapped=false and writes NOTHING.
//   - ATOMIC: the load-compare-store is serialized across processes on the
//     per-namespace lock.StateKV(ns) — a contended namespace surfaces
//     *lock.HeldError (the CLI maps it to exit 6), never a torn read/write.
//   - PERSIST: a successful swap is durable (a fresh KVGet reads it back).
//
// Non-vacuity mutant (registered, primary): in KVCompareAndSwap drop the
// comparison and store newval unconditionally (return true always). The
// "mismatch must not write" and "absent-sentinel on a present key" fitnesses go
// RED. Alternate mutant: remove the lock.Acquire and operate lock-free → the
// "serializes on the namespace lock" fitness no longer sees *lock.HeldError → RED.

func TestKVCompareAndSwap(t *testing.T) {
	l := bootstrapLayout(t)

	// (1) CAS from absent with the AbsentSentinel succeeds and persists — even
	// though Bootstrap never created the kv/ subdir (the CAS lazily creates it).
	if swapped, err := state.KVCompareAndSwap(l, "ports", "alpha", state.AbsentSentinel, "3000", "op1"); err != nil || !swapped {
		t.Fatalf("CAS absent→3000: swapped=%v err=%v, want swapped=true nil", swapped, err)
	}
	if v, ok, err := state.KVGet(l, "ports", "alpha"); err != nil || !ok || v != "3000" {
		t.Fatalf(`after set: KVGet=%q ok=%v err=%v, want "3000" true nil`, v, ok, err)
	}

	// (2) CAS with a matching expected swaps.
	if swapped, err := state.KVCompareAndSwap(l, "ports", "alpha", "3000", "4000", "op2"); err != nil || !swapped {
		t.Fatalf("CAS 3000→4000: swapped=%v err=%v, want true nil", swapped, err)
	}
	if v, _, _ := state.KVGet(l, "ports", "alpha"); v != "4000" {
		t.Fatalf("after matched swap: KVGet=%q, want 4000", v)
	}

	// (3) CAS with a mismatched expected must NOT write.
	if swapped, err := state.KVCompareAndSwap(l, "ports", "alpha", "9999", "5000", "op3"); err != nil || swapped {
		t.Fatalf("CAS mismatch: swapped=%v err=%v, want false nil", swapped, err)
	}
	if v, _, _ := state.KVGet(l, "ports", "alpha"); v != "4000" {
		t.Fatalf("mismatch wrote anyway: KVGet=%q, want unchanged 4000", v)
	}

	// (4) AbsentSentinel against a now-present key fails (expected absent, but present).
	if swapped, err := state.KVCompareAndSwap(l, "ports", "alpha", state.AbsentSentinel, "6000", "op4"); err != nil || swapped {
		t.Fatalf("CAS absent-sentinel on present key: swapped=%v err=%v, want false nil", swapped, err)
	}
	if v, _, _ := state.KVGet(l, "ports", "alpha"); v != "4000" {
		t.Fatalf("absent-sentinel on present key wrote anyway: KVGet=%q, want 4000", v)
	}

	// (5) Namespaces are independent: a CAS on "hosts" is governed by its own
	// key/store and leaves "ports" untouched.
	if swapped, err := state.KVCompareAndSwap(l, "hosts", "alpha", state.AbsentSentinel, "db-1", "op5"); err != nil || !swapped {
		t.Fatalf("CAS hosts absent→db-1: swapped=%v err=%v, want true nil", swapped, err)
	}
	if v, _, _ := state.KVGet(l, "ports", "alpha"); v != "4000" {
		t.Fatalf("cross-namespace bleed: ports/alpha=%q, want 4000", v)
	}
}

func TestKVGetAbsent(t *testing.T) {
	l := bootstrapLayout(t)
	// Missing namespace (no store file yet): a pure read — ok=false, no error.
	if v, ok, err := state.KVGet(l, "never", "alpha"); err != nil || ok || v != "" {
		t.Fatalf(`KVGet missing namespace = %q ok=%v err=%v, want "" false nil`, v, ok, err)
	}
	// Existing namespace, missing key.
	if _, err := state.KVCompareAndSwap(l, "ports", "alpha", state.AbsentSentinel, "3000", "op1"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := state.KVGet(l, "ports", "missing"); err != nil || ok || v != "" {
		t.Fatalf(`KVGet missing key = %q ok=%v err=%v, want "" false nil`, v, ok, err)
	}
}

// TestKVCompareAndSwapSerializesOnLock proves the CAS takes lock.StateKV(ns)
// around its load-compare-store: with that exact key already held, the CAS does
// not block or tear — it surfaces *lock.HeldError (→ exit 6), the uniform wi
// contended-lock posture. This is what makes two agents' CASes on one namespace
// linearize rather than race.
func TestKVCompareAndSwapSerializesOnLock(t *testing.T) {
	l := bootstrapLayout(t)
	key, err := lock.StateKV("ports")
	if err != nil {
		t.Fatal(err)
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		t.Fatalf("pre-acquire ports lock: %v", err)
	}
	defer func() { _ = held.Release() }()

	swapped, err := state.KVCompareAndSwap(l, "ports", "alpha", state.AbsentSentinel, "3000", "op-contend")
	var he *lock.HeldError
	if !errors.As(err, &he) {
		t.Fatalf("CAS on a held namespace: err=%v, want *lock.HeldError", err)
	}
	if swapped {
		t.Fatalf("CAS on a held namespace reported swapped=true; must be false (no write under contention)")
	}
}

func bootstrapLayout(t *testing.T) layout.Layout {
	t.Helper()
	l, err := layout.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	return l
}
