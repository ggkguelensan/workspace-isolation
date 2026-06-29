package layout

import (
	"path/filepath"
	"testing"
)

// LAYOUT-PATHS / LAYOUT-SAFE (unit, level: path construction) — DESIGN §1, §4.
//
// internal/layout is the SOLE place a wi path string is built; no other package
// joins paths. This guard pins the exact on-disk scheme with hand-written golden
// relative paths (an independent copy of the layout, so any drift in a segment
// literal or join order in layout.go reddens it) and pins that every path
// constructor rejects unsafe path segments (traversal, absolute, separators,
// empty, dot) while still ACCEPTING an ordinary name.
//
// Non-vacuity (guard→mutant):
//   - LAYOUT-PATHS: change any segment literal ("isolas"→"isolate") or swap a
//     join order (Isolate(task,repo)→repo,task) → TestPaths RED.
//   - LAYOUT-SAFE: make validSegment always return nil → the reject cases of
//     TestSegmentSafety RED; make it always return an error → the accept case
//     ("a" must be allowed) RED. Pinned from both sides so it can't go vacuous.

const goldenRoot = "/prj"

// rel joins goldenRoot with the given OS-native segments to form the expected
// absolute path, mirroring how the constructors are specified WITHOUT reusing
// their code.
func rel(segs ...string) string {
	all := append([]string{goldenRoot}, segs...)
	return filepath.Join(all...)
}

func TestPaths(t *testing.T) {
	l, err := New(goldenRoot)
	if err != nil {
		t.Fatalf("New(%q): %v", goldenRoot, err)
	}

	// Single-segment, no-input accessors.
	fixed := []struct {
		name string
		got  string
		want string
	}{
		{"Root", l.Root(), goldenRoot},
		{"Config", l.Config(), rel("wi.config.jsonc")},
		{"ReposDir", l.ReposDir(), rel("repos")},
		{"IsolasDir", l.IsolasDir(), rel("isolas")},
		{"WiDir", l.WiDir(), rel(".wi")},
		{"LocksDir", l.LocksDir(), rel(".wi", "locks")},
		{"StateDir", l.StateDir(), rel(".wi", "state")},
		{"LogDir", l.LogDir(), rel(".wi", "log")},
		{"MirrorsDir", l.MirrorsDir(), rel(".wi", "mirrors")},
		{"LandDir", l.LandDir(), rel(".wi", "land")},
		{"PortsDir", l.PortsDir(), rel(".wi", "ports")},
		{"TrustDir", l.TrustDir(), rel(".wi", "trust")},
	}
	for _, c := range fixed {
		if c.got != c.want {
			t.Errorf("%s() = %q, want %q", c.name, c.got, c.want)
		}
	}

	// Input-bearing constructors (must succeed for valid names).
	repo, err := l.Repo("alpha")
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if want := rel("repos", "alpha"); repo != want {
		t.Errorf("Repo(alpha) = %q, want %q", repo, want)
	}

	task, err := l.TaskDir("X")
	if err != nil {
		t.Fatalf("TaskDir: %v", err)
	}
	if want := rel("isolas", "X"); task != want {
		t.Errorf("TaskDir(X) = %q, want %q", task, want)
	}

	iso, err := l.Isolate("X", "alpha")
	if err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	if want := rel("isolas", "X", "alpha"); iso != want {
		t.Errorf("Isolate(X, alpha) = %q, want %q", iso, want)
	}
}

func TestSegmentSafety(t *testing.T) {
	l, err := New(goldenRoot)
	if err != nil {
		t.Fatal(err)
	}

	// The accept side (non-vacuity floor): an ordinary name must NOT be rejected.
	if _, err := l.Repo("ok-name_1"); err != nil {
		t.Fatalf("Repo(ok-name_1) wrongly rejected: %v", err)
	}

	bad := []string{
		"",            // empty
		".",           // current dir
		"..",          // parent — the traversal we must block
		"a/b",         // embedded forward slash
		"a\\b",        // embedded backslash
		"/abs",        // absolute
		"a\x00b",      // NUL byte
		"../escape",   // traversal prefix
		"sub/../../x", // sneaky traversal
	}
	for _, name := range bad {
		t.Run("repo/"+name, func(t *testing.T) {
			if p, err := l.Repo(name); err == nil {
				t.Errorf("Repo(%q) = %q, want error (unsafe segment)", name, p)
			}
		})
		t.Run("task/"+name, func(t *testing.T) {
			if p, err := l.TaskDir(name); err == nil {
				t.Errorf("TaskDir(%q) = %q, want error (unsafe segment)", name, p)
			}
		})
	}

	// Isolate must reject if EITHER segment is unsafe.
	if _, err := l.Isolate("..", "alpha"); err == nil {
		t.Error("Isolate(.., alpha) = nil error, want error on bad task")
	}
	if _, err := l.Isolate("X", ".."); err == nil {
		t.Error("Isolate(X, ..) = nil error, want error on bad repo")
	}
}

func TestNewRequiresAbsolute(t *testing.T) {
	if _, err := New("relative/path"); err == nil {
		t.Error("New(relative/path) = nil error, want error (root must be absolute)")
	}
	if _, err := New(""); err == nil {
		t.Error("New(empty) = nil error, want error")
	}
	if _, err := New(goldenRoot); err != nil {
		t.Errorf("New(%q) wrongly rejected: %v", goldenRoot, err)
	}
}
