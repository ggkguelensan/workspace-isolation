//go:build unix

package lock

import (
	"fmt"
	"os"

	"github.com/ggkguelensan/workspace-isolation/internal/host"
)

// ProvenDead reports whether holder's process is provably no longer running — the
// holder-liveness limb of lock self-heal (DESIGN §7.3). Together with the fs-trust
// gate (applied separately by the break path), a true result is the ONLY thing that
// authorizes wi to break a contended lock, so the predicate is deliberately
// conservative: every holder it cannot PROVE dead resolves to false (not breakable),
// because a false positive would steal a lock from a running peer.
//
// Per DESIGN §7.3, on the SAME host a holder is proven dead when:
//   - its boot_id differs from this boot's — the machine rebooted since the lock was
//     taken, so the recorded process is certainly gone (and its pid may since have
//     been reused, which is exactly why a bare pid is not a sufficient key); or
//   - its boot_id matches this boot AND processAlive(pid) is false — Kill(pid,0)
//     returned ESRCH, the pid no longer exists this boot.
//
// It returns false (never proven dead) for: a holder on a DIFFERENT host (its
// pid/boot_id name a process on another machine we cannot probe; a lock dir
// reachable from two hosts is a shared fs the fs-trust gate refuses anyway); an
// empty host or empty boot_id (unknown origin we cannot reason about); and a
// non-positive pid in the same-boot case (a corrupt holder body). A body-less or
// unparseable lock never reaches here — the break path treats an unknown holder
// (a ReadHolder error) as not-dead.
func ProvenDead(h Holder) (bool, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return false, fmt.Errorf("lock: proven-dead: hostname: %w", err)
	}
	if h.Host == "" || h.Host != hostname {
		return false, nil // another (or unknown) machine — cannot prove dead
	}
	bootID, err := host.BootID()
	if err != nil {
		return false, fmt.Errorf("lock: proven-dead: boot id: %w", err)
	}
	if h.BootID == "" {
		return false, nil // unknown boot — cannot reason, never break
	}
	if h.BootID != bootID {
		return true, nil // rebooted since acquire → the recorded holder is gone
	}
	// Same host, same boot: the recorded pid is meaningful this boot.
	return h.PID > 0 && !processAlive(h.PID), nil
}
