package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
)

// AbsentSentinel is the reserved `expected` value for KVCompareAndSwap and the
// frozen `wi state cas --expected __ABSENT__` flag (DESIGN §8): it means "swap
// ONLY if the key is currently absent" — the claim-a-slot-exactly-once primitive
// an agent uses to acquire a resource without racing. It is deliberately a value
// no real caller would store, so it can never collide with a legitimate value.
const AbsentSentinel = "__ABSENT__"

// kvSubdir is the state subdirectory holding one JSON store per KV namespace.
// Bootstrap does NOT create it (it is not a WiSubdirs entry); KVCompareAndSwap
// creates it lazily on first write. Because it is a directory, state.List — which
// skips directories — never mistakes it for a "<task>.json" registry record.
const kvSubdir = "kv"

// kvPath returns the store file for namespace within stateDir (layout.StateDir()):
// <stateDir>/kv/<namespace>.json. namespace is validated through layout's single
// traversal chokepoint because it becomes a path segment, exactly as recordPath
// validates a task name — state owns the flat "<namespace>.json" naming here.
func kvPath(stateDir, namespace string) (string, error) {
	if err := layout.ValidateSegment("namespace", namespace); err != nil {
		return "", err
	}
	return filepath.Join(stateDir, kvSubdir, namespace+".json"), nil
}

// kvLoad reads a namespace's whole key→value map. A namespace with no store file
// yet is the empty map, not an error — the same idempotent "nothing here yet"
// posture as a never-created isolate. It is a pure local read.
func kvLoad(stateDir, namespace string) (map[string]string, error) {
	p, err := kvPath(stateDir, namespace)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("state: read kv store %s: %w", p, err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("state: parse kv store %s: %w", p, err)
	}
	return m, nil
}

// kvStore writes a namespace's whole map back through the single .wi/ atomic
// writer (DESIGN §6.2), lazily creating the kv/ subdir (Bootstrap does not). Map
// keys marshal in sorted order, so the on-disk bytes are deterministic for a
// given map — a torn or partial write is impossible (create-temp-then-rename).
func kvStore(stateDir, namespace string, m map[string]string) error {
	p, err := kvPath(stateDir, namespace)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("state: create kv dir %s: %w", filepath.Dir(p), err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal kv store %s: %w", p, err)
	}
	data = append(data, '\n')
	if err := lockfs.WriteFileAtomic(p, data, 0o644); err != nil {
		return fmt.Errorf("state: write kv store %s: %w", p, err)
	}
	return nil
}

// KVGet reads namespace/key from the namespaced KV store under l.StateDir(). ok
// is false when the namespace has no store yet or the key is absent. It is a pure
// local read taking NO lock: KVCompareAndSwap is the only writer and it writes
// atomically, so a concurrent reader sees either the pre- or the post-CAS map,
// never a torn one.
func KVGet(l layout.Layout, namespace, key string) (value string, ok bool, err error) {
	m, err := kvLoad(l.StateDir(), namespace)
	if err != nil {
		return "", false, err
	}
	value, ok = m[key]
	return value, ok, nil
}

// KVCompareAndSwap atomically sets namespace/key to newval IFF its current value
// equals expected — with expected == AbsentSentinel meaning "iff the key is
// currently absent". It serializes the load-compare-store across processes on
// lock.StateKV(namespace), wi's single cross-process serializer, so two agents'
// CASes on one namespace linearize while CASes on different namespaces do not
// contend. A contended namespace surfaces *lock.HeldError (the CLI maps it to
// exit 6), never a torn read/write. swapped reports whether the write happened: a
// mismatch returns (false, nil) and writes NOTHING. opID is stamped into the held
// lock as best-effort holder identity, exactly as isolate.New.
//
// This owns its lock end-to-end (acquire→compare→store→release), unlike the
// registry primitives whose concurrency is the caller's job (see package doc): a
// CAS's lock scope is EXACTLY the load-compare-store, so making it self-contained
// is what guarantees the compare and the conditional write are one indivisible
// step against concurrent agents.
func KVCompareAndSwap(l layout.Layout, namespace, key, expected, newval, opID string) (swapped bool, err error) {
	k, err := lock.StateKV(namespace)
	if err != nil {
		return false, err
	}
	held, err := lock.Acquire(l.LocksDir(), k)
	if err != nil {
		return false, err // *lock.HeldError on contention → CLI exit 6
	}
	defer func() { _ = held.Release() }()
	_ = held.Stamp(opID) // best-effort holder metadata (see isolate.New)

	m, err := kvLoad(l.StateDir(), namespace)
	if err != nil {
		return false, err
	}
	cur, present := m[key]
	if expected == AbsentSentinel {
		if present {
			return false, nil // expected absent, but a value is present → no swap
		}
	} else if !present || cur != expected {
		return false, nil // expected a specific value that is not the current one → no swap
	}

	m[key] = newval
	if err := kvStore(l.StateDir(), namespace, m); err != nil {
		return false, err
	}
	return true, nil
}
