//go:build unix

package lock

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with the given pid currently exists on
// THIS machine. It is the proven-dead gate of lock self-heal (DESIGN §2 / §7.3):
// wi auto-breaks a stale lock ONLY when its recorded holder is provably dead, so
// this predicate must never report a live process as dead — that would let wi
// steal a lock from a running peer. It is therefore deliberately conservative:
// every ambiguity resolves to "alive".
//
// It probes with signal 0, which runs all of kill(2)'s error checking for signal
// delivery but sends no signal:
//   - nil    the process exists and we may signal it          → alive
//   - ESRCH  no such process                                  → dead
//   - EPERM  the process exists but is owned by another user  → alive
//   - other  unexpected errno; stay conservative              → alive
//
// pid <= 0 is rejected up front: 0 and negative pids address process GROUPS in
// kill(2), not a single process, so probing them is meaningless and a corrupt
// holder pid must never read as alive on that path.
//
// PID reuse across reboots makes a bare pid an unsound liveness key on its own;
// the lock-liveness layer pairs this with the holder's boot_id (open decision #3)
// before trusting a "dead" verdict to break a lock. This function answers only
// the narrow local-pid question.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	// EPERM (exists, not ours) or any unexpected errno: stay conservative.
	return true
}
