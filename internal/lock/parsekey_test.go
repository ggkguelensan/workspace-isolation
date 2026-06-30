package lock

import "testing"

// Guard LOCK-PARSE-KEY: ParseKey is the inverse of the key namespace — it reconstructs
// a typed Key from its canonical String(), validating the embedded name segment exactly
// as the constructors do. `lock ls` uses it to turn a "<key>.lock" filename back into a
// Key (so a stray, non-key file in the locks dir is REJECTED rather than assessed), so
// the load-bearing properties are (a) round-trip identity across all three namespaces
// and (b) rejection of anything that is not a valid key in the closed namespace.
//
// Non-vacuity mutant (registered): replace the `default` error branch with
// `return Repo(s)` — i.e. treat any unrecognized string as a repo key. A junk filename
// like "garbage" then parses successfully → TestParseKey RED on the rejection cases
// (`ParseKey("garbage") = nil error, want error`). Alternate: drop the "isolate-state:"
// branch (fall through to the default error) → the isolate-state round-trip reddens.
func TestParseKey(t *testing.T) {
	// Round-trip: every constructor output must parse back to the identical key.
	api, err := Repo("api")
	if err != nil {
		t.Fatal(err)
	}
	iso, err := IsolateState("feature-x")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Key{ProjectRegistry(), Workspace(), api, iso} {
		got, err := ParseKey(want.String())
		if err != nil {
			t.Errorf("ParseKey(%q): unexpected error %v", want.String(), err)
			continue
		}
		if got.String() != want.String() {
			t.Errorf("ParseKey(%q).String() = %q, want %q (round-trip must be identity)",
				want.String(), got.String(), want.String())
		}
	}

	// Rejection: anything that is not a valid key in the closed namespace is an error,
	// never a fabricated Key — a stray file must not be treated as a lock.
	for _, bad := range []string{
		"",               // empty
		"garbage",        // no namespace prefix
		"repo:",          // empty repo segment
		"repo:bad/name",  // segment with a path separator
		"isolate-state:", // empty task segment
		"unknown:thing",  // unrecognized namespace
	} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) = nil error, want error (not a valid key)", bad)
		}
	}
}
