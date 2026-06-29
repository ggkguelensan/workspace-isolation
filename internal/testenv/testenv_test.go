package testenv

import (
	"path/filepath"
	"testing"
)

// TESTENV-HERMETIC (test-harness fitness) — supports PLAN §M0 real-git harness
// (t.TempDir origins, git-config isolation, EvalSymlinks root) and DESIGN §2
// determinism. testenv is the sandbox every FS/git unit test runs inside, so its
// own correctness — reproducible + isolated — must itself be guarded.
//
// Pins:
//   - determinism: seeding the SAME origin twice yields the IDENTICAL base-tip
//     SHA (only possible if identity AND author/committer dates are fixed). This
//     is the headline guard.
//   - isolation+identity: a commit is made with NO global/system git config
//     (GIT_CONFIG_GLOBAL=/dev/null, NOSYSTEM) yet succeeds, and its author is the
//     injected deterministic identity — proving the ambient ~/.gitconfig neither
//     leaks in nor is required.
//   - EvalSymlinks root: Root is fully symlink-resolved (matters on macOS where
//     t.TempDir lives under /var -> /private/var).
//
// Non-vacuity (guard→mutant): remove the fixed GIT_AUTHOR_DATE/GIT_COMMITTER_DATE
// from hermeticEnv → commit dates follow the wall clock → the SHA no longer
// equals goldenBaseSHA → TestSeedOriginIsDeterministic RED. (A relative
// "two runs agree" check is NOT enough: two seedings in the same wall-clock
// second collide by luck, so we pin the ABSOLUTE expected SHA instead.) Drop the
// GIT_AUTHOR_NAME injection → the ambient system username leaks into the author →
// TestHermeticIdentity RED.

// goldenBaseSHA is the sha1 of the seeded commit, fully determined by the fixed
// identity + dates + the "wi seed\n" README + the "seed" message (git's default
// object format is sha1). Any drift in those inputs changes it.
const goldenBaseSHA = "48f4258c3d1798b23ee2fff07d59044f3b24850d"

func TestSeedOriginIsDeterministic(t *testing.T) {
	e := New(t)
	o := e.SeedOrigin(t, "repo")
	sha := e.Git(t, o, "rev-parse", "refs/heads/"+DefaultBranch)
	if sha != goldenBaseSHA {
		t.Errorf("seeded base SHA = %s, want %s\n(hermetic identity/date fixing broken, or seed content changed)", sha, goldenBaseSHA)
	}
}

func TestHermeticIdentity(t *testing.T) {
	e := New(t)
	o := e.SeedOrigin(t, "repo")

	if an := e.Git(t, o, "log", "-1", "--format=%an"); an != authorName {
		t.Errorf("commit author name = %q, want %q (ambient git config leaked, or env identity missing?)", an, authorName)
	}
	if ae := e.Git(t, o, "log", "-1", "--format=%ae"); ae != authorEmail {
		t.Errorf("commit author email = %q, want %q", ae, authorEmail)
	}
}

func TestRootNormalized(t *testing.T) {
	e := New(t)
	resolved, err := filepath.EvalSymlinks(e.Root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != e.Root {
		t.Errorf("Root is not symlink-normalized: %q resolves to %q", e.Root, resolved)
	}
	if !filepath.IsAbs(e.Root) {
		t.Errorf("Root %q is not absolute", e.Root)
	}
}
