package lock

import (
	"path/filepath"
	"testing"
)

// Guard LOCK-KEYS / LOCK-ORDER (unit) — DESIGN §6.1.
//
// internal/lock is the SOLE owner of wi's advisory lock-key namespace. The
// canonical key STRINGS are a wire contract, not an implementation detail: sync
// and land must derive the byte-identical "repo:<name>" key, or the freshness
// race they linearize on reopens. Multi-acquire folds any key set into one total
// order (sorted, deduped) so two processes grabbing overlapping sets can never
// take the underlying lock files in conflicting orders.
//
// Non-vacuity:
//   - LOCK-KEYS: change a kind prefix ("repo:"→…) or the ".lock" suffix → the
//     pinned-string assertions go RED.
//   - LOCK-ORDER: make orderedUnique return its input unsorted → the
//     opposite-order inputs stop matching → RED; drop the dedup → the duplicate
//     survives, length differs → RED.

func TestCanonicalKeyStrings(t *testing.T) {
	if got := ProjectRegistry().String(); got != "project-registry" {
		t.Errorf("ProjectRegistry = %q, want project-registry", got)
	}
	r, err := Repo("alpha")
	if err != nil {
		t.Fatalf("Repo(alpha): %v", err)
	}
	if got := r.String(); got != "repo:alpha" {
		t.Errorf("Repo(alpha) = %q, want repo:alpha", got)
	}
	is, err := IsolateState("T1")
	if err != nil {
		t.Fatalf("IsolateState(T1): %v", err)
	}
	if got := is.String(); got != "isolate-state:T1" {
		t.Errorf("IsolateState(T1) = %q, want isolate-state:T1", got)
	}
}

func TestKeyConstructorsRejectUnsafeNames(t *testing.T) {
	for _, bad := range []string{"", "..", ".", "a/b", "/abs", "a\x00b"} {
		if _, err := Repo(bad); err == nil {
			t.Errorf("Repo(%q) = nil error, want rejection", bad)
		}
		if _, err := IsolateState(bad); err == nil {
			t.Errorf("IsolateState(%q) = nil error, want rejection", bad)
		}
	}
	// Accept floor: a plain safe segment must be allowed.
	if _, err := Repo("ok_1"); err != nil {
		t.Errorf("Repo(ok_1) wrongly rejected: %v", err)
	}
}

func TestKeyPathDerivation(t *testing.T) {
	r, _ := Repo("alpha")
	got := r.Path("/p/.wi/locks")
	want := filepath.Join("/p/.wi/locks", "repo:alpha.lock")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestOrderedUniqueIsTotalOrderAndDedups(t *testing.T) {
	a, _ := Repo("a")
	b, _ := Repo("b")
	reg := ProjectRegistry()

	// Same set, different argument orders → identical canonical sequence.
	o1 := orderedUnique([]Key{b, reg, a})
	o2 := orderedUnique([]Key{a, b, reg})
	o3 := orderedUnique([]Key{reg, b, a, b, a}) // with duplicates
	if !keysEqual(o1, o2) {
		t.Fatalf("orderedUnique not order-independent:\n o1=%v\n o2=%v", o1, o2)
	}
	if !keysEqual(o1, o3) {
		t.Fatalf("orderedUnique did not dedup to canonical sequence:\n o1=%v\n o3=%v", o1, o3)
	}
	// Strictly ascending by canonical string (a true total order, no equal pairs).
	for i := 1; i < len(o1); i++ {
		if o1[i-1].String() >= o1[i].String() {
			t.Errorf("not strictly ascending at %d: %q !< %q", i, o1[i-1], o1[i])
		}
	}
	if len(o3) != 3 {
		t.Errorf("dedup: len=%d, want 3", len(o3))
	}
}

func keysEqual(x, y []Key) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i].String() != y[i].String() {
			return false
		}
	}
	return true
}
