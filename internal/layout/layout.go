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
// New is the pure, deterministic path core and requires an already-absolute root.
// Resolve adds DESIGN §4 symlink normalization (it requires an existing root), and
// Bootstrap materializes the .wi/ runtime subtree on disk — both filesystem-aware
// constructors the CLI uses at startup.
package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
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

// Resolve returns a Layout rooted at root with every symlink in the path resolved
// (DESIGN §4): the canonical root all later paths derive from, so equality checks
// against it are reliable — notably on macOS, where temp and working directories
// live under /var, a symlink to /private/var. root must already exist and be
// absolute; use New for the existence-agnostic path core.
func Resolve(root string) (Layout, error) {
	if !filepath.IsAbs(root) {
		return Layout{}, fmt.Errorf("layout: root %q is not absolute", root)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Layout{}, fmt.Errorf("layout: resolve root %q: %w", root, err)
	}
	return New(resolved)
}

// Root returns the absolute project root.
func (l Layout) Root() string { return l.root }

// wiGitignore is the content of .wi/.gitignore: a single "*" so the entire
// runtime subtree is self-ignored regardless of the project's root .gitignore,
// guaranteeing machine state is never committed (DESIGN §1) without wi having to
// touch the user's own ignore files.
const wiGitignore = "*\n"

// Bootstrap materializes the .wi/ runtime subtree — WiDir plus every WiSubdirs
// entry — and writes the self-ignoring .wi/.gitignore. It is idempotent (safe to
// re-run on an initialized project) and is the first consumer of the single
// atomic .wi/ writer (DESIGN §6.2). It does NOT create repos/ or isolas/; those
// appear as repos are cloned and isolates created.
func (l Layout) Bootstrap() error {
	if err := os.MkdirAll(l.WiDir(), 0o755); err != nil {
		return fmt.Errorf("layout: bootstrap %s: %w", l.WiDir(), err)
	}
	for _, sub := range WiSubdirs() {
		d := filepath.Join(l.WiDir(), sub)
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("layout: bootstrap %s: %w", d, err)
		}
	}
	gi := filepath.Join(l.WiDir(), ".gitignore")
	if err := lockfs.WriteFileAtomic(gi, []byte(wiGitignore), 0o644); err != nil {
		return fmt.Errorf("layout: bootstrap %s: %w", gi, err)
	}
	return nil
}

// Config returns the path to the committed manifest, <root>/wi.config.jsonc.
func (l Layout) Config() string { return filepath.Join(l.root, "wi.config.jsonc") }

// ReposDir returns <root>/repos, the SSOT reference-clone parent.
func (l Layout) ReposDir() string { return filepath.Join(l.root, "repos") }

// IsolasDir returns <root>/isolas, the per-task isolate parent.
func (l Layout) IsolasDir() string { return filepath.Join(l.root, "isolas") }

// WiDir returns <root>/.wi, the machine runtime-state subtree.
func (l Layout) WiDir() string { return filepath.Join(l.root, ".wi") }

// The .wi/ runtime-state subdirectories (DESIGN §1). WiSubdirs is the single
// source of truth for the subtree Bootstrap creates; the per-dir accessors below
// derive from the same names.
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
// order. Bootstrap creates exactly these.
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
