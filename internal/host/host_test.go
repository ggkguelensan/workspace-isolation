package host

import "testing"

// Guard HOST-BOOTID (M4, open decision #3): BootID must return a NON-EMPTY,
// boot-STABLE identifier on the supported platforms (linux, darwin). It is the
// reuse guard the lock-liveness layer pairs with the holder pid: a lock whose
// boot_id differs from the current boot was written before a reboot, so its pid
// is meaningless and the holder is provably gone (DESIGN §6 / §7.3). The two
// properties that matter — and that this fitness pins platform-agnostically —
// are non-emptiness (callers can always record an id) and stability within a
// boot (two derivations in the same boot must agree, or the comparison the lock
// layer makes would spuriously declare every holder stale).
//
// Non-vacuity mutant (registered): make the platform `bootID()` return
// `"", nil` → the non-empty assertion below Fatals RED. Alternate: have it
// return a value that varies per call (e.g. append a call counter) → the
// stability assertion RED. Confirmed RED with the empty-return mutant before
// this guard went green.
func TestBootID(t *testing.T) {
	id1, err := BootID()
	if err != nil {
		t.Fatalf("BootID() error: %v", err)
	}
	if id1 == "" {
		t.Fatal(`BootID() = "", want a non-empty per-boot identifier`)
	}

	// Stable within a boot: a second derivation must match the first, because the
	// lock layer compares a recorded boot_id against a freshly derived one.
	id2, err := BootID()
	if err != nil {
		t.Fatalf("BootID() second call error: %v", err)
	}
	if id1 != id2 {
		t.Errorf("BootID() not stable across calls: %q != %q", id1, id2)
	}
}
