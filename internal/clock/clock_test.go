package clock

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/ggkguelensan/workspace-isolation/internal/cli/opid"
)

// CLOCK-DETERMINISM (unit, level: determinism seam) — DESIGN §2 (determinism),
// §4 (internal/clock = injectable time / op_id randomness).
//
// wi envelopes must be byte-identical across runs modulo op_id + timestamps
// (DESIGN §2). The two volatile inputs — wall-clock time and randomness — are
// funneled through a Clock so golden tests can inject a Fake and get a fixed,
// reproducible stream. This guard pins that the Fake is:
//   - reproducible: same (instant, seed) -> identical byte stream AND identical
//     op_ids when fed to opid.New (the determinism the goldens depend on);
//   - seed-sensitive: different seeds -> different streams (so a mutant that
//     ignores the seed can't masquerade as deterministic);
//   - non-degenerate: the stream is not all-zero.
// and that the System clock is live (crypto/rand varies; Now is UTC).
//
// Non-vacuity (guard→mutant): make Fake.Rand return crypto/rand.Reader, or make
// the deterministic reader ignore its seed -> TestFakeReproducible /
// TestFakeSeedSensitive RED.

// Compile-time proof both impls satisfy Clock.
var (
	_ Clock = System{}
	_ Clock = (*Fake)(nil)
)

const fixedInstant = "2026-06-29T12:00:00Z"

func mustInstant(t *testing.T) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, fixedInstant)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func read(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatalf("ReadFull(%d): %v", n, err)
	}
	return b
}

func TestFakeReproducible(t *testing.T) {
	inst := mustInstant(t)

	a := NewFake(inst, 1)
	b := NewFake(inst, 1)

	// Identical byte streams.
	if ba, bb := read(t, a.Rand(), 64), read(t, b.Rand(), 64); !bytes.Equal(ba, bb) {
		t.Errorf("same (instant, seed) produced differing streams:\n %x\n %x", ba, bb)
	}

	// Identical op_ids end-to-end through opid.New (fresh fakes, same config).
	a2, b2 := NewFake(inst, 7), NewFake(inst, 7)
	ida, err := opid.New(a2.Now(), a2.Rand())
	if err != nil {
		t.Fatal(err)
	}
	idb, err := opid.New(b2.Now(), b2.Rand())
	if err != nil {
		t.Fatal(err)
	}
	if ida != idb {
		t.Errorf("opid not reproducible under identical Fake: %q vs %q", ida, idb)
	}
}

func TestFakeSeedSensitive(t *testing.T) {
	inst := mustInstant(t)
	s1 := read(t, NewFake(inst, 1).Rand(), 64)
	s2 := read(t, NewFake(inst, 2).Rand(), 64)
	if bytes.Equal(s1, s2) {
		t.Error("different seeds produced identical streams — seed is ignored")
	}
}

func TestFakeStreamNonDegenerate(t *testing.T) {
	inst := mustInstant(t)
	s := read(t, NewFake(inst, 1).Rand(), 64)
	if bytes.Equal(s, make([]byte, 64)) {
		t.Error("Fake stream is all zeros — degenerate randomness")
	}
}

func TestFakeNowAndAdvance(t *testing.T) {
	inst := mustInstant(t)
	f := NewFake(inst, 1)
	if !f.Now().Equal(inst) {
		t.Errorf("Now() = %v, want %v", f.Now(), inst)
	}
	f.Advance(90 * time.Minute)
	if want := inst.Add(90 * time.Minute); !f.Now().Equal(want) {
		t.Errorf("after Advance, Now() = %v, want %v", f.Now(), want)
	}
}

func TestSystemIsLive(t *testing.T) {
	var c Clock = System{}

	// Now is UTC and plausibly recent (after the project's birth year).
	now := c.Now()
	if loc := now.Location(); loc != time.UTC {
		t.Errorf("System.Now() location = %v, want UTC", loc)
	}
	if now.Year() < 2026 {
		t.Errorf("System.Now() = %v, implausibly old", now)
	}

	// Two reads from crypto/rand should differ (collision is ~1 in 2^64).
	r1, r2 := read(t, c.Rand(), 16), read(t, c.Rand(), 16)
	if bytes.Equal(r1, r2) {
		t.Error("System.Rand() returned identical 16-byte reads — not live randomness")
	}
}
