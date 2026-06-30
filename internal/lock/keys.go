// Package lock is the SOLE owner of wi's advisory lock-key namespace and the
// single total order in which any set of those keys is acquired (DESIGN §6.1).
// It sits directly on internal/lockfs.FileLock (flock(2)) and names the closed
// set of resources concurrent wi processes serialize on:
//
//	workspace              the whole project — the coarsest, workspace-wide key,
//	                       held alone and OUTERMOST (e.g. by the offline startup
//	                       recovery pass) so a maintenance sweep does not interleave
//	                       with a concurrent command's finer-grained locks
//	project-registry       writes to the project registry
//	repo:<name>            a repo's base-ref mutation — sync and land take the
//	                       IDENTICAL key, which is what linearizes the freshness
//	                       race between fetching and landing (DESIGN §6.1)
//	isolate-state:<name>   a single task's isolate state
//
// Acquisition is non-blocking (TryLock): a contended key surfaces as *HeldError,
// which the CLI maps to exit 6 lock_held. Multi-key acquires fold the request
// into orderedUnique order so two processes contending on an overlapping set can
// never take the underlying files in conflicting orders. The §7.3 auto-break
// self-heal for dead holders is a later (M4) refinement layered on top.
package lock

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// lockSuffix is appended to a key's canonical string to form its lock filename.
const lockSuffix = ".lock"

// The closed key namespace, named once so the constructors (which build a key string)
// and ParseKey (which reverses one) can never drift apart. project-registry is a bare
// constant key; the other two are "<prefix><safe-segment>".
const (
	workspaceKey       = "workspace"
	projectRegistryKey = "project-registry"
	repoPrefix         = "repo:"
	isolateStatePrefix = "isolate-state:"
)

// Key is one key in the closed lock namespace. Its String() is a stable wire
// contract (sync and land must produce byte-identical repo:<name> keys), and the
// key derives its own lock-file name, so callers never assemble lock paths
// themselves. The zero Key is not valid; use the constructors.
type Key struct {
	s string
}

// String returns the canonical key string (e.g. "repo:alpha").
func (k Key) String() string { return k.s }

// Path returns the lock file backing this key within locksDir, which is
// layout.LocksDir() of the target project. The canonical string is a safe single
// path component (names are validated at construction), so this never escapes
// locksDir.
func (k Key) Path(locksDir string) string {
	return filepath.Join(locksDir, k.s+lockSuffix)
}

// Workspace returns the workspace-wide key — the coarsest lock in the namespace,
// guarding a whole-project maintenance operation (the offline startup recovery pass).
// It is a bare constant key like ProjectRegistry (no name segment). It is intended to
// be held ALONE and OUTERMOST: a holder acquires it before the command body and may
// then take finer-grained keys (e.g. isolate-state:<task>) nested inside, never the
// reverse, so the workspace lock can never deadlock against a command's own locks.
func Workspace() Key { return Key{s: workspaceKey} }

// ProjectRegistry returns the key guarding writes to the project registry.
func ProjectRegistry() Key { return Key{s: projectRegistryKey} }

// Repo returns the key guarding repo's base-ref mutation. sync and land MUST use
// this same key for a given repo. name must be a safe path segment.
func Repo(name string) (Key, error) {
	if err := layout.ValidateSegment("repo", name); err != nil {
		return Key{}, err
	}
	return Key{s: repoPrefix + name}, nil
}

// ParseKey reverses String(): it reconstructs a typed Key from its canonical string,
// the inverse of the constructors, validating the embedded name segment exactly as the
// constructor would. It is how the lock lister turns a "<key>.lock" filename back into
// a Key, so a stray non-key file in the locks dir is rejected (error) rather than
// assessed. The namespaces are the only valid inputs:
//
//	"workspace"             -> Workspace()
//	"project-registry"      -> ProjectRegistry()
//	"repo:<name>"           -> Repo(name)
//	"isolate-state:<name>"  -> IsolateState(name)
//
// An unrecognized prefix, or a name segment that fails validation (empty, traversal,
// separator), is an error — ParseKey never fabricates a Key for an input String()
// could not have produced.
func ParseKey(s string) (Key, error) {
	switch {
	case s == workspaceKey:
		return Workspace(), nil
	case s == projectRegistryKey:
		return ProjectRegistry(), nil
	case strings.HasPrefix(s, repoPrefix):
		return Repo(strings.TrimPrefix(s, repoPrefix))
	case strings.HasPrefix(s, isolateStatePrefix):
		return IsolateState(strings.TrimPrefix(s, isolateStatePrefix))
	default:
		return Key{}, fmt.Errorf("lock: %q is not a valid key", s)
	}
}

// IsolateState returns the key guarding a single task's isolate state. task must
// be a safe path segment.
func IsolateState(task string) (Key, error) {
	if err := layout.ValidateSegment("task", task); err != nil {
		return Key{}, err
	}
	return Key{s: isolateStatePrefix + task}, nil
}

// orderedUnique returns keys sorted ascending by canonical string with duplicates
// removed: the single total order every multi-key acquire follows. Sorting is
// what guarantees two acquirers of an overlapping set request the underlying
// locks in the same sequence; deduping prevents a request from self-contending on
// a repeated key (two FileLocks on one path are distinct open file descriptions
// and would flock-exclude each other).
func orderedUnique(keys []Key) []Key {
	out := make([]Key, len(keys))
	copy(out, keys)
	sort.Slice(out, func(i, j int) bool { return out[i].s < out[j].s })
	n := 0
	for i := range out {
		if i == 0 || out[i].s != out[n-1].s {
			out[n] = out[i]
			n++
		}
	}
	return out[:n]
}
