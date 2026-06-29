// Package layout is the SOLE owner of every filesystem path under a wi project
// root (DESIGN §1, §4). No other package joins path segments: callers ask a
// Layout for a path and receive an absolute, validated string. Centralizing
// construction here is what lets the SSOT/isolate scheme be pinned by one guard
// and keeps user-supplied names (repo, task) from escaping the tree via path
// traversal.
//
// The on-disk model (DESIGN §1):
//
//	<root>/
//	  wi.config.jsonc        committed manifest
//	  repos/<repo>/          SSOT reference clones
//	  isolas/<task>/<repo>/  per-task worktree isolates
//	  .wi/                   machine runtime state (gitignored)
//	    locks/ state/ log/ mirrors/ land/ ports/ trust/
//
// Symlink normalization of the root (DESIGN §4 "EvalSymlinks-normalized") and
// the filesystem-touching Bootstrap of the .wi/ subtree are layered on at the
// CLI boundary in a later unit (they require an existing on-disk root); New here
// is the pure, deterministic path core and requires an already-absolute root.
package layout

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Layout resolves wi paths beneath a single absolute project root.
type Layout struct {
	root string // absolute, cleaned
}

// New returns a Layout rooted at an absolute path. The root is cleaned but its
// existence is not checked here. It is an error to pass a relative or empty root.
func New(root string) (Layout, error) {
	if root == "" {
		return Layout{}, fmt.Errorf("layout: empty root")
	}
	if !filepath.IsAbs(root) {
		return Layout{}, fmt.Errorf("layout: root %q is not absolute", root)
	}
	return Layout{root: filepath.Clean(root)}, nil
}

// Root returns the absolute project root.
func (l Layout) Root() string { return l.root }

// Config returns the path to the committed manifest, <root>/wi.config.jsonc.
func (l Layout) Config() string { return filepath.Join(l.root, "wi.config.jsonc") }

// ReposDir returns <root>/repos, the SSOT reference-clone parent.
func (l Layout) ReposDir() string { return filepath.Join(l.root, "repos") }

// IsolasDir returns <root>/isolas, the per-task isolate parent.
func (l Layout) IsolasDir() string { return filepath.Join(l.root, "isolas") }

// WiDir returns <root>/.wi, the machine runtime-state subtree.
func (l Layout) WiDir() string { return filepath.Join(l.root, ".wi") }

// The .wi/ runtime-state subdirectories (DESIGN §1). WiSubdirs is the single
// source of truth for the subtree a future Bootstrap will create; the per-dir
// accessors below derive from the same names.
const (
	subLocks   = "locks"
	subState   = "state"
	subLog     = "log"
	subMirrors = "mirrors"
	subLand    = "land"
	subPorts   = "ports"
	subTrust   = "trust"
)

// WiSubdirs returns the names of every .wi/ runtime subdirectory, in a stable
// order. Bootstrap (later unit) creates exactly these.
func WiSubdirs() []string {
	return []string{subLocks, subState, subLog, subMirrors, subLand, subPorts, subTrust}
}

// LocksDir returns <root>/.wi/locks.
func (l Layout) LocksDir() string { return filepath.Join(l.WiDir(), subLocks) }

// StateDir returns <root>/.wi/state.
func (l Layout) StateDir() string { return filepath.Join(l.WiDir(), subState) }

// LogDir returns <root>/.wi/log.
func (l Layout) LogDir() string { return filepath.Join(l.WiDir(), subLog) }

// MirrorsDir returns <root>/.wi/mirrors.
func (l Layout) MirrorsDir() string { return filepath.Join(l.WiDir(), subMirrors) }

// LandDir returns <root>/.wi/land.
func (l Layout) LandDir() string { return filepath.Join(l.WiDir(), subLand) }

// PortsDir returns <root>/.wi/ports.
func (l Layout) PortsDir() string { return filepath.Join(l.WiDir(), subPorts) }

// TrustDir returns <root>/.wi/trust.
func (l Layout) TrustDir() string { return filepath.Join(l.WiDir(), subTrust) }

// Repo returns <root>/repos/<repo>, the SSOT clone for repo. The name must be a
// single safe path segment.
func (l Layout) Repo(repo string) (string, error) {
	if err := validSegment("repo", repo); err != nil {
		return "", err
	}
	return filepath.Join(l.ReposDir(), repo), nil
}

// TaskDir returns <root>/isolas/<task>, the isolate dir for a task. The name
// must be a single safe path segment.
func (l Layout) TaskDir(task string) (string, error) {
	if err := validSegment("task", task); err != nil {
		return "", err
	}
	return filepath.Join(l.IsolasDir(), task), nil
}

// Isolate returns <root>/isolas/<task>/<repo>, a single repo's worktree within a
// task's isolate. Both names must be safe segments.
func (l Layout) Isolate(task, repo string) (string, error) {
	if err := validSegment("task", task); err != nil {
		return "", err
	}
	if err := validSegment("repo", repo); err != nil {
		return "", err
	}
	return filepath.Join(l.IsolasDir(), task, repo), nil
}

// validSegment rejects any string that is not a single, safe path component:
// empty, "." / "..", containing a path separator (either flavor) or NUL, or
// absolute. This is the chokepoint that prevents user-supplied repo/task names
// from escaping the project tree via traversal.
func validSegment(kind, s string) error {
	switch {
	case s == "":
		return fmt.Errorf("layout: empty %s name", kind)
	case s == "." || s == "..":
		return fmt.Errorf("layout: %s name %q is a path-traversal segment", kind, s)
	case strings.ContainsRune(s, 0):
		return fmt.Errorf("layout: %s name %q contains a NUL byte", kind, s)
	case strings.ContainsAny(s, `/\`) || strings.ContainsRune(s, filepath.Separator):
		return fmt.Errorf("layout: %s name %q contains a path separator", kind, s)
	case filepath.IsAbs(s):
		return fmt.Errorf("layout: %s name %q is absolute", kind, s)
	}
	return nil
}
