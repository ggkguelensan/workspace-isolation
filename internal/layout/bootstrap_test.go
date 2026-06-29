package layout

import (
	"os"
	"path/filepath"
	"testing"
)

// LAYOUT-BOOTSTRAP (unit, level: on-disk skeleton) — DESIGN §1, §4, §6.2.
//
// Resolve is the EvalSymlinks-normalized constructor the CLI uses at startup, so
// the root every later path derives from is canonical and equality checks against
// it are reliable (on macOS temp/working dirs live under /var, a symlink to
// /private/var). Bootstrap is the ONLY code that materializes the .wi/ runtime
// subtree, and it drops a self-ignoring .wi/.gitignore so machine state can never
// be committed (DESIGN §1). Bootstrap is the first real consumer of
// lockfs.WriteFileAtomic — the single .wi/ writer (DESIGN §6.2).
//
// Non-vacuity (guard→mutant):
//   - Bootstrap creates only WiDir and skips the WiSubdirs loop → a declared
//     subdir is missing → TestBootstrapCreatesSubtree RED.
//   - Resolve returns New(root) without EvalSymlinks → through a symlinked root
//     Root() keeps the link component → TestResolveNormalizesSymlinks RED.

func mustDir(t *testing.T, p string) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	if !fi.IsDir() {
		t.Errorf("%s exists but is not a directory", p)
	}
}

func TestResolveNormalizesSymlinks(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	l, err := Resolve(link)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", link, err)
	}

	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if l.Root() != want {
		t.Errorf("Resolve(link).Root() = %q, want fully-resolved %q", l.Root(), want)
	}
	// The resolved root must be a symlink fixed point.
	if again, _ := filepath.EvalSymlinks(l.Root()); again != l.Root() {
		t.Errorf("Root() %q is not symlink-canonical (resolves further to %q)", l.Root(), again)
	}
}

func TestResolveRejectsRelativeAndMissing(t *testing.T) {
	if _, err := Resolve("rel/path"); err == nil {
		t.Error("Resolve(relative) = nil error, want error (root must be absolute)")
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := Resolve(missing); err == nil {
		t.Error("Resolve(missing) = nil error, want error (EvalSymlinks needs existence)")
	}
}

func TestBootstrapCreatesSubtree(t *testing.T) {
	root := t.TempDir()
	l, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}

	if err := l.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	mustDir(t, l.WiDir())
	for _, sub := range WiSubdirs() {
		mustDir(t, filepath.Join(l.WiDir(), sub))
	}

	gi := filepath.Join(l.WiDir(), ".gitignore")
	got, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read .wi/.gitignore: %v", err)
	}
	if string(got) != "*\n" {
		t.Errorf(".wi/.gitignore = %q, want %q (runtime state must self-ignore)", got, "*\n")
	}

	// Idempotent: a second Bootstrap over an existing tree is a no-op success.
	if err := l.Bootstrap(); err != nil {
		t.Fatalf("second Bootstrap (idempotency): %v", err)
	}
}
