package lock

import (
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
)

// Stamp records the current process's holder identity (CurrentHolder for opID)
// into the body of EVERY lock this Held owns, so a later contender can read who
// holds a lock and judge its liveness (DESIGN §6 / §7.3). It is called after a
// successful Acquire, while the flocks are still held; the body is written in
// place on each locked inode (lockfs.FileLock.WriteBody). The holder is computed
// once and shared by all keys — every lock taken in one Acquire belongs to the
// same operation. If the holder cannot be determined (host introspection) or any
// write fails, Stamp returns an error and the locks remain held: the caller
// decides whether to proceed unstamped or release. Stamping is best-effort
// metadata, not the mutual-exclusion guarantee — exclusion is the flock itself.
func (h *Held) Stamp(opID string) error {
	holder, err := CurrentHolder(opID)
	if err != nil {
		return fmt.Errorf("lock: stamp holder: %w", err)
	}
	body, err := holder.Marshal()
	if err != nil {
		return fmt.Errorf("lock: stamp holder: %w", err)
	}
	for _, l := range h.locks {
		if err := l.WriteBody(body); err != nil {
			return fmt.Errorf("lock: stamp holder into %s: %w", l.Path(), err)
		}
	}
	return nil
}

// ReadHolder reads the holder identity recorded in the lock file for key, without
// taking the flock — exactly how a process that lost the TryLock race inspects the
// holder it could not displace (advisory flock does not block reads). A missing
// lock file, or a lock that was acquired but never stamped (empty body), is an
// error rather than a zero-value Holder: an unreadable holder is treated as
// unknown and conservatively never broken (DESIGN §6 self-heal). A missing file's
// error unwraps to os.ErrNotExist so a caller can distinguish "no such lock" from
// "lock held but its body is unreadable."
func ReadHolder(locksDir string, key Key) (Holder, error) {
	body, err := lockfs.ReadBodyAt(key.Path(locksDir))
	if err != nil {
		return Holder{}, fmt.Errorf("lock: read holder for %q: %w", key.String(), err)
	}
	h, err := ParseHolder(body)
	if err != nil {
		return Holder{}, fmt.Errorf("lock: read holder for %q: %w", key.String(), err)
	}
	return h, nil
}
