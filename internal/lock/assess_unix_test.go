//go:build unix

package lock

import (
	"os"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/host"
)

// Guard LOCK-SAFE-TO-BREAK (M4 self-heal, DESIGN §7.3 / §7.4 HEAL-3): the read-only
// break decision composes the two independent gates — FSTrustworthy(locksDir) AND a
// KNOWN holder (ReadHolder ok) that is ProvenDead — into one verdict. The composition
// is conjunctive and fail-safe: Safe is true ONLY when all three hold, and an unknown
// holder (a body-less / unparseable lock → ReadHolder error) is NEVER safe to break.
//
// Non-vacuity mutant (registered): drop the ProvenDead conjunct from the verdict, i.e.
// `Safe = FSTrustworthy && HolderKnown` — break any known holder on a trustworthy fs,
// alive or not. The live-holder case (this very process) then reads Safe=true → RED.
// That is the dangerous direction the guard exists to forbid: stealing a lock from a
// running peer. Alternate mutant: treat an unknown holder as breakable → the
// unknown-holder case reddens.
func TestAssessBreak(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	bootID, err := host.BootID()
	if err != nil {
		t.Fatalf("boot id: %v", err)
	}

	key, err := Repo("api")
	if err != nil {
		t.Fatalf("Repo key: %v", err)
	}

	writeBody := func(t *testing.T, locksDir string, h Holder) {
		t.Helper()
		body, err := h.Marshal()
		if err != nil {
			t.Fatalf("marshal holder: %v", err)
		}
		// AssessBreak reads the body at key.Path without taking the flock, exactly as
		// a contender that lost the TryLock race would inspect the holder it could not
		// displace, so writing the body file directly faithfully sets up the lock.
		if err := os.WriteFile(key.Path(locksDir), body, 0o600); err != nil {
			t.Fatalf("write lock body: %v", err)
		}
	}

	t.Run("unknown holder is never breakable", func(t *testing.T) {
		locksDir := t.TempDir() // trustworthy fs, but no lock body written
		d, err := AssessBreak(locksDir, key)
		if err != nil {
			t.Fatalf("AssessBreak: %v", err)
		}
		if d.HolderKnown {
			t.Errorf("HolderKnown = true, want false (no body)")
		}
		if d.Safe {
			t.Errorf("Safe = true for an unknown holder, want false (DESIGN §7.3: never break an unknown holder)")
		}
	})

	t.Run("live holder is never breakable", func(t *testing.T) {
		locksDir := t.TempDir()
		// CurrentHolder names THIS running process: same host, same boot, live pid.
		h, err := CurrentHolder("op_live")
		if err != nil {
			t.Fatalf("CurrentHolder: %v", err)
		}
		writeBody(t, locksDir, h)
		d, err := AssessBreak(locksDir, key)
		if err != nil {
			t.Fatalf("AssessBreak: %v", err)
		}
		if !d.HolderKnown {
			t.Errorf("HolderKnown = false, want true (body written)")
		}
		if d.ProvenDead {
			t.Errorf("ProvenDead = true for this live process, want false")
		}
		if d.Safe {
			t.Errorf("Safe = true while the holder (this process) is alive, want false — must never steal a lock from a running peer")
		}
	})

	t.Run("proven-dead holder on a trustworthy fs is breakable", func(t *testing.T) {
		locksDir := t.TempDir()
		// Same host, a DIFFERENT boot id → the machine rebooted since acquire, so the
		// recorded holder is certainly gone (ProvenDead's boot-mismatch limb).
		dead := Holder{PID: 999999, Host: hostname, BootID: bootID + "-stale", OpID: "op_dead"}
		writeBody(t, locksDir, dead)
		d, err := AssessBreak(locksDir, key)
		if err != nil {
			t.Fatalf("AssessBreak: %v", err)
		}
		if !d.FSTrustworthy {
			t.Fatalf("FSTrustworthy = false for a local temp dir, want true (test precondition)")
		}
		if !d.HolderKnown {
			t.Errorf("HolderKnown = false, want true")
		}
		if !d.ProvenDead {
			t.Errorf("ProvenDead = false for a boot-mismatched holder, want true")
		}
		if !d.Safe {
			t.Errorf("Safe = false for a proven-dead holder on a trustworthy fs, want true")
		}
	})
}
