package baseref_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/baseref"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard BASEREF-RESOLVE — the manifest→effective-base seam (DESIGN §1). A base
// declared as an ordered candidate list resolves against the repo's already-cloned
// single-branch SSOT mirror: the first candidate that exists locally wins (so a
// mirror cloned on "main" resolves ["dev","main"] to main; once "dev" also exists,
// the earlier candidate wins). When the mirror is absent or holds none of the
// candidates, Resolve falls back to the top preference candidates[0] and lets
// downstream ref resolution surface any real error — it never guesses a branch the
// mirror doesn't have when the mirror IS readable.
//
// Non-vacuity: (primary — consult the mirror) make Resolve `return candidates[0]`
// unconditionally → the main-only mirror resolves ["dev","main"] to "dev" → RED,
// proving the mirror is actually read; (alternate — keep the fallback) on found ==
// false return "" → the no-mirror repo yields "" not "dev" → RED, proving the
// optimistic fallback to candidates[0].

func initMirror(t *testing.T, env *testenv.Env, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir mirror: %v", err)
	}
	env.Git(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("c0"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	env.Git(t, dir, "add", "f.txt")
	env.Git(t, dir, "commit", "-m", "c0")
	env.Git(t, dir, "switch", "--detach") // SSOT posture
}

func TestResolve(t *testing.T) {
	env := testenv.New(t)
	l, err := layout.New(env.Root)
	if err != nil {
		t.Fatalf("layout.New: %v", err)
	}
	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	dir, err := l.Repo("api")
	if err != nil {
		t.Fatalf("layout.Repo: %v", err)
	}
	initMirror(t, env, dir) // mirror tracks main only

	// main-only mirror: ["dev","main"] resolves to the one branch it has.
	if got := baseref.Resolve(ctx, g, l, "api", []string{"dev", "main"}); got != "main" {
		t.Errorf("Resolve(main-only) = %q, want main", got)
	}

	// dev now exists too: the earlier candidate wins.
	env.Git(t, dir, "branch", "dev", "main")
	if got := baseref.Resolve(ctx, g, l, "api", []string{"dev", "main"}); got != "dev" {
		t.Errorf("Resolve(dev+main) = %q, want dev", got)
	}

	// No mirror on disk for this repo: fall back to the top preference.
	if got := baseref.Resolve(ctx, g, l, "ghost", []string{"dev", "main"}); got != "dev" {
		t.Errorf("Resolve(no-mirror) = %q, want dev (candidates[0] fallback)", got)
	}
}
