//go:build unix

package lock

import (
	"os"
	"os/exec"
	"testing"
)

// Guard LOCK-LIVENESS-PID (M4, Wave C self-heal): processAlive must answer the
// narrow "does a process with this pid exist on this machine right now" question
// correctly in BOTH directions, because it is the proven-dead gate that lock
// self-heal consults before breaking a stale lock (DESIGN §2 / §7.3). The
// dangerous failure is reporting a LIVE process as dead — that would let wi steal
// a lock from a running peer — so the live-self case is the one the predicate
// must never get wrong, and the predicate is deliberately conservative (any
// ambiguity resolves to alive).
//
// Non-vacuity mutant (registered): make processAlive `return true`
// unconditionally — the reaped-child case below (a provably dead pid) then reads
// as alive and this fitness reddens. Symmetrically, replacing the body with
// `return false` reddens the live-self case. Confirmed RED with `return true`
// before this guard went green.
func TestProcessAlive(t *testing.T) {
	// Our own process is unquestionably alive.
	if !processAlive(os.Getpid()) {
		t.Errorf("processAlive(self pid %d) = false, want true", os.Getpid())
	}

	// A child we start AND reap is provably dead: once Run (which waits) returns,
	// the kernel has released the pid, so kill(pid, 0) must report no such
	// process. PID reuse could in principle hand the number to a new process, but
	// a single short-lived test will not hit that window.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		// `true` should always be present and succeed; if the platform cannot run
		// it, skip rather than fail spuriously.
		t.Skipf("could not run a throwaway child to reap: %v", err)
	}
	dead := cmd.Process.Pid
	if processAlive(dead) {
		t.Errorf("processAlive(reaped child pid %d) = true, want false", dead)
	}

	// pid <= 0 address process GROUPS in kill(2), not a single process; reject so
	// a zero/negative holder pid in a corrupt lock body can never read as alive.
	if processAlive(0) {
		t.Errorf("processAlive(0) = true, want false")
	}
	if processAlive(-1) {
		t.Errorf("processAlive(-1) = true, want false")
	}
}
