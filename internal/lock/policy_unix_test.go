//go:build unix

package lock

import (
	"os"
	"os/exec"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/host"
)

// Guard LOCK-PROVEN-DEAD (M4 self-heal): ProvenDead is the holder-liveness judgment
// that — together with the fs-trust gate, applied separately by the break path —
// authorizes breaking a contended lock (DESIGN §7.3). It must be RIGHT in every
// limb, and above all must NEVER report a live holder as dead — that would steal a
// lock from a running peer — so the live-self case is load-bearing.
//
// Per DESIGN §7.3 a holder is proven dead iff, on the SAME host, EITHER its boot_id
// mismatches this boot (the machine rebooted → the holder is gone, regardless of
// whether its pid has since been reused by a live process) OR its boot_id matches
// and Kill(pid,0)==ESRCH. A different host, or an unknown host/boot, is never proven
// dead.
//
// Non-vacuity mutant (registered): drop the boot_id-mismatch limb (fall through to
// the same-boot pid check for every same-host holder) → the different-boot/live-pid
// case below reads as NOT dead (its pid IS this live process) → reddens the reboot
// assertion. Alternate: drop the `h.Host != hostname` guard → the foreign-host
// reaped-pid case reads as dead → reddens the foreign-host assertion. Both confirmed
// RED before green.
func TestProvenDead(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	bootID, err := host.BootID()
	if err != nil {
		t.Fatalf("boot id: %v", err)
	}

	// (1) This very process: same host, same boot, alive → NEVER proven dead. This
	// is the dangerous direction — a false positive here breaks a live peer's lock.
	self := Holder{PID: os.Getpid(), Host: hostname, BootID: bootID, OpID: "op_self"}
	if dead, err := ProvenDead(self); err != nil || dead {
		t.Errorf("ProvenDead(self) = %v (err %v), want false — must never break a live holder's lock", dead, err)
	}

	// A child we start AND reap is a provably-dead pid this boot (the kernel has
	// released the number once Run, which waits, returns).
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("could not run a throwaway child to reap: %v", err)
	}
	deadPID := cmd.Process.Pid

	// (2) Same host, same boot, reaped pid → proven dead (the ESRCH limb).
	gone := Holder{PID: deadPID, Host: hostname, BootID: bootID, OpID: "op_gone"}
	if dead, err := ProvenDead(gone); err != nil || !dead {
		t.Errorf("ProvenDead(same-boot reaped pid %d) = %v (err %v), want true", deadPID, dead, err)
	}

	// (3) Same host, DIFFERENT boot, but the pid is THIS LIVE process → still proven
	// dead: a boot_id mismatch means the machine rebooted, so the holder that took
	// the lock is gone even though this pid number is alive again now (pid reuse is
	// exactly why a bare pid is an unsound key across reboots).
	rebooted := Holder{PID: os.Getpid(), Host: hostname, BootID: bootID + "-PRIOR-BOOT", OpID: "op_reboot"}
	if dead, err := ProvenDead(rebooted); err != nil || !dead {
		t.Errorf("ProvenDead(different-boot, live pid) = %v (err %v), want true (reboot proves death despite pid reuse)", dead, err)
	}

	// (4) DIFFERENT host, even with a reaped pid → never proven dead: we cannot probe
	// another machine, and a lock dir reachable from two hosts is a shared fs the
	// fs-trust gate refuses to auto-break anyway.
	foreign := Holder{PID: deadPID, Host: hostname + "-elsewhere", BootID: bootID, OpID: "op_foreign"}
	if dead, err := ProvenDead(foreign); err != nil || dead {
		t.Errorf("ProvenDead(foreign host) = %v (err %v), want false", dead, err)
	}

	// (5) Unknown origin (empty host / empty boot) → never proven dead: a corrupt or
	// partial holder body we cannot reason about must not authorize a break.
	if dead, err := ProvenDead(Holder{PID: deadPID, Host: "", BootID: bootID}); err != nil || dead {
		t.Errorf("ProvenDead(empty host) = %v (err %v), want false", dead, err)
	}
	if dead, err := ProvenDead(Holder{PID: deadPID, Host: hostname, BootID: ""}); err != nil || dead {
		t.Errorf("ProvenDead(empty boot) = %v (err %v), want false", dead, err)
	}
}
