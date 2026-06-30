//go:build unix

package lock

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// LockStatus is the read-only status of one lock present in a locks directory: its key
// paired with the break assessment (DESIGN §7.3 / §7.4). It is the per-lock row `lock ls`
// projects into its envelope and that `lock break` gates on.
type LockStatus struct {
	Key      Key
	Decision BreakDecision
}

// List enumerates every lock present in locksDir and assesses each, returning one
// LockStatus per recognized lock file sorted by canonical key. It is read-only: it takes
// no flock and writes nothing — each AssessBreak only statfs's the dir and reads a body,
// exactly how a contender inspects a lock it does not hold.
//
// A locksDir that does not exist yields an empty result, not an error — no project has
// been initialized, or no lock has ever been taken; "no locks" is a valid state. A file
// whose name is not "<key>.lock" for a key in the closed namespace (ParseKey rejects it)
// is silently skipped: the locks dir may legitimately hold unrelated files, and a stray
// must never be fabricated into a lock. Subdirectories and non-".lock" entries are
// likewise ignored. Only a genuine I/O fault (reading the dir, or an AssessBreak whose
// statfs/host-introspection fails) is returned as an error.
func List(locksDir string) ([]LockStatus, error) {
	entries, err := os.ReadDir(locksDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("lock: list %q: %w", locksDir, err)
	}

	var out []LockStatus
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, lockSuffix) {
			continue
		}
		key, err := ParseKey(strings.TrimSuffix(name, lockSuffix))
		if err != nil {
			continue // not a key in the closed namespace — a stray file, never a lock
		}
		d, err := AssessBreak(locksDir, key)
		if err != nil {
			return nil, fmt.Errorf("lock: list %q: assess %q: %w", locksDir, key.String(), err)
		}
		out = append(out, LockStatus{Key: key, Decision: d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out, nil
}
