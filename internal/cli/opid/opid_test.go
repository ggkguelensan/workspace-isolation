package opid

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"
)

// OPID-FORMAT (unit, level: wire format) — DESIGN §3.1 / §8 op_id row.
//
// op_id is the one volatile envelope field (DESIGN §2 determinism: envelopes are
// byte-identical across runs MODULO op_id). Its shape is frozen: a root id is
// "op_<base36ts>_<base32rand>" and a child job appends ".<n>". This guard pins
// that shape from independent angles so any drift in the prefix, the time unit,
// the base, the random width, or the case reddens it:
//   - the random half is pinned by feeding all-zero bytes: base32 of 5 zero
//     bytes, lowercased, no padding, is exactly "aaaaaaaa" (pins encoding +
//     lowercase + width + no-pad in one shot);
//   - the timestamp half is pinned by round-tripping through the INVERSE
//     function (ParseInt base 36) and asserting it equals now.UnixMilli() — which
//     also distinguishes milliseconds from seconds (would drop the .123) and from
//     nanoseconds (would be ~1e6× larger).
//
// Non-vacuity (guard→mutant): change the time unit (UnixMilli→Unix), the prefix
// ("op_"→"wi_"), randLen (5→4), or drop the lowercasing → TestNewFormat and/or
// TestValid go RED. TestValidRejects proves Valid is not constant-true.

// A fixed instant with sub-second precision so the ms-vs-s/ns unit is observable.
const fixedMillis = int64(1_700_000_000_123)

func zeroSource(n int) *bytes.Reader { return bytes.NewReader(make([]byte, n)) }

func TestNewFormat(t *testing.T) {
	now := time.UnixMilli(fixedMillis)
	id, err := New(now, zeroSource(randLen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !Valid(id) {
		t.Fatalf("New produced id %q that fails Valid", id)
	}
	if !strings.HasPrefix(id, "op_") {
		t.Fatalf("id %q lacks op_ prefix", id)
	}

	// id == "op_" + <ts> + "_" + <rand>; neither segment contains '_'.
	body := strings.TrimPrefix(id, "op_")
	ts, rnd, ok := strings.Cut(body, "_")
	if !ok {
		t.Fatalf("id %q has no ts_rand separator", id)
	}

	// Random half: 5 zero bytes -> "aaaaaaaa".
	if rnd != "aaaaaaaa" {
		t.Errorf("random segment = %q, want %q (base32 of 5 zero bytes, lowercased, no padding)", rnd, "aaaaaaaa")
	}

	// Timestamp half: inverse-decode and assert it is the input in MILLISECONDS.
	v, err := strconv.ParseInt(ts, 36, 64)
	if err != nil {
		t.Fatalf("ts segment %q is not base36: %v", ts, err)
	}
	if v != fixedMillis {
		t.Errorf("decoded ts = %d, want %d (UnixMilli) — wrong base or time unit", v, fixedMillis)
	}
}

func TestNewDeterministic(t *testing.T) {
	now := time.UnixMilli(fixedMillis)
	a, err := New(now, zeroSource(randLen))
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(now, zeroSource(randLen))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("New not deterministic under identical inputs: %q vs %q", a, b)
	}
}

func TestNewRequiresFullRandomness(t *testing.T) {
	// A short source must be an error, never a truncated id.
	if id, err := New(time.UnixMilli(fixedMillis), zeroSource(randLen-1)); err == nil {
		t.Errorf("New with short randomness = %q, want error", id)
	}
}

func TestChild(t *testing.T) {
	now := time.UnixMilli(fixedMillis)
	root, err := New(now, zeroSource(randLen))
	if err != nil {
		t.Fatal(err)
	}

	c1, err := Child(root, 1)
	if err != nil {
		t.Fatalf("Child(root, 1): %v", err)
	}
	if want := root + ".1"; c1 != want {
		t.Errorf("Child(root, 1) = %q, want %q", c1, want)
	}
	if !Valid(c1) {
		t.Errorf("child id %q fails Valid", c1)
	}

	// Children nest.
	c2, err := Child(c1, 2)
	if err != nil {
		t.Fatalf("Child(c1, 2): %v", err)
	}
	if want := root + ".1.2"; c2 != want {
		t.Errorf("Child(c1, 2) = %q, want %q", c2, want)
	}
	if !Valid(c2) {
		t.Errorf("nested child id %q fails Valid", c2)
	}

	// Index must be >= 1.
	if _, err := Child(root, 0); err == nil {
		t.Error("Child(root, 0) = nil error, want error (index must be >= 1)")
	}
	if _, err := Child(root, -1); err == nil {
		t.Error("Child(root, -1) = nil error, want error")
	}

	// Parent must be a valid op_id.
	if _, err := Child("not-an-opid", 1); err == nil {
		t.Error("Child(bogus, 1) = nil error, want error (invalid parent)")
	}
}

func TestValidAccepts(t *testing.T) {
	good := []string{
		"op_1abc_aaaaaaaa",
		"op_1abc_aaaaaaaa.1",
		"op_1abc_aaaaaaaa.1.2.34",
		"op_0_bcde2345", // minimal ts, full alphabet sample in rand
	}
	for _, id := range good {
		if !Valid(id) {
			t.Errorf("Valid(%q) = false, want true", id)
		}
	}
}

func TestValidRejects(t *testing.T) {
	bad := []string{
		"",                    // empty
		"op_",                 // prefix only
		"1abc_aaaaaaaa",       // no prefix
		"op__aaaaaaaa",        // empty ts
		"op_1abc_",            // empty rand
		"op_1abc_AAAAAAAA",    // uppercase rand (must be lowercased)
		"op_1abc_aaaaaaa",     // 7-char rand (too short)
		"op_1abc_aaaaaaaaa",   // 9-char rand (too long)
		"op_1abc_aaaaaaa1",    // '1' not in base32 alphabet
		"op_1abc_aaaaaaaa.",   // trailing dot
		"op_1abc_aaaaaaaa.0",  // zero child index
		"op_1abc_aaaaaaaa.01", // leading-zero child index
		"op_1abc_aaaaaaaa .1", // embedded space
	}
	for _, id := range bad {
		if Valid(id) {
			t.Errorf("Valid(%q) = true, want false", id)
		}
	}
	// Non-vacuity floor: Valid must accept at least one well-formed id, so the
	// rejects above can't be passing via a constant-false matcher.
	if !Valid("op_1abc_aaaaaaaa") {
		t.Fatal("Valid is constant-false: rejects a well-formed op_id")
	}
}
