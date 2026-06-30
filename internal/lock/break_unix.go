//go:build unix

package lock

import (
	"errors"
	"fmt"
	"os"
)

// Break is the ACTION counterpart to List (DESIGN §7.3 / HEAL-3): it displaces a stale
// lock at key in locksDir, but ONLY when doing so is provably safe. It first runs the
// full read-only AssessBreak gate and removes the lock file solely when the verdict is
// Safe — i.e. the holder is PROVEN DEAD (boot_id mismatch, or same-boot ESRCH) on a
// flock-trustworthy local filesystem. When the verdict is not Safe — a live holder, an
// unknown/body-less holder, or an fs where flock is not known trustworthy — it removes
// NOTHING and returns AssessBreak's verdict unchanged; the caller maps that to exit 6
// lock_held.
//
// Removing the lock file is the displacement: a proven-dead holder no longer holds the
// flock (the kernel released it when the process died, or the reboot that changed the
// boot_id wiped all flock state — see lockfs/flock_unix.go), so the only artifact left is
// the on-disk file carrying the dead holder's stale body. Unlinking it lets the next
// Acquire O_CREATE a fresh file and take the flock cleanly. The Safe gate is load-bearing:
// unlinking a file a LIVE peer still holds would break mutual exclusion — a subsequent
// Acquire would create a NEW inode and flock that, not the live holder's — which is
// exactly the data-loss path DESIGN §7 forbids.
//
// The returned BreakDecision is AssessBreak's verdict (on a safe break it describes the
// now-removed holder, which is what authorized the break). A lock file that has already
// vanished between the assessment and the unlink is not an error — there is nothing left
// to break. Only an AssessBreak fault or an unexpected unlink fault is returned as error.
func Break(locksDir string, key Key) (BreakDecision, error) {
	d, err := AssessBreak(locksDir, key)
	if err != nil {
		return BreakDecision{}, fmt.Errorf("lock: break %q: %w", key.String(), err)
	}
	if !d.Safe {
		return d, nil
	}
	if err := os.Remove(key.Path(locksDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return BreakDecision{}, fmt.Errorf("lock: break %q: remove lock file: %w", key.String(), err)
	}
	return d, nil
}
