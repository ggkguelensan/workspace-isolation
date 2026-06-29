package lockfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/fault"
)

// HEAL-ATOMIC-WRITE (fitness, level: crash-safety) — DESIGN §6.2.
//
// WriteFileAtomic is the SINGLE atomic writer every .wi/ writer reuses. Its
// contract: a reader of the target path NEVER sees a torn/partial file — it sees
// either the complete old content or the complete new content. This guard is the
// first consumer of the WI_FAULT seam (internal/fault): it injects a crash
// between the temp write and the rename and asserts the target is untouched.
//
// Non-vacuity (guard→mutant): make WriteFileAtomic write the target in place
// (open O_TRUNC on the final path, or os.WriteFile) instead of temp+rename →
// under the injected crash the target is left truncated/torn ("v2…", or empty) →
// TestAtomicReplaceIsCrashSafe RED. TestWriteSucceedsWhenFaultInactive is the
// two-sided floor: with no fault the replace DOES apply, so the crash-safety test
// can't be passing merely because writes never take effect.

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, tmpPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("leftover temp files in %s: %v", dir, matches)
	}
}

func TestWriteFileAtomicWritesContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	want := []byte(`{"k":1}` + "\n")

	if err := WriteFileAtomic(p, want, 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("content = %q, want %q", got, want)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("perm = %o, want 0644", fi.Mode().Perm())
	}
	assertNoTemps(t, dir)
}

func TestAtomicReplaceIsCrashSafe(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	if err := WriteFileAtomic(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Crash between temp-write and rename.
	t.Setenv(fault.EnvVar, FaultBeforeRename)
	err := WriteFileAtomic(p, []byte("v2-deliberately-much-longer"), 0o644)
	if err == nil {
		t.Fatal("expected injected crash error, got nil (fault seam did not fire)")
	}

	// The target must still be exactly the OLD content — never torn, partial, or empty.
	got, rerr := os.ReadFile(p)
	if rerr != nil {
		t.Fatalf("target unreadable after crashed replace: %v", rerr)
	}
	if string(got) != "v1" {
		t.Errorf("after crashed replace, content = %q, want %q — atomic replace was torn", got, "v1")
	}
	// The aborted write must not litter temp files.
	assertNoTemps(t, dir)
}

func TestWriteSucceedsWhenFaultInactive(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	if err := WriteFileAtomic(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "v2" {
		t.Errorf("content = %q, want v2 (replace did not apply)", got)
	}
	assertNoTemps(t, dir)
}
