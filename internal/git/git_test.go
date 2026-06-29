package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard GIT-FF-ONLY — the SSOT keystone (DESIGN §5).
//
// FastForwardBaseRef is the SOLE path that mutates a base ref anywhere in wi.
// The SSOT clone is a detached HEAD at the base tip; both v0 sync and v1 land
// advance refs/heads/<base> identically, and ONLY by fast-forward:
//
//	git merge-base --is-ancestor <current> <new>   # ff-safety
//	git update-ref refs/heads/<base> <new>          # no checkout, no merge
//
// A true fast-forward advances the ref; anything that is NOT a fast-forward
// (divergent or rewinding target) is refused with the base ref left untouched.
// This append-only discipline is what guarantees the SSOT base is never rewound
// or forked under a racing op.
//
// Non-vacuity: drop the --is-ancestor precheck (unconditionally update-ref) →
// the divergent target wrongly advances the ref → TestFastForwardRefusesNonFastForward
// RED on both the missing error and the moved ref. The positive test is the other
// side of the floor (a mutant that always refuses fails it).

func writeCommit(t *testing.T, env *testenv.Env, dir, file, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	env.Git(t, dir, "add", file)
	env.Git(t, dir, "commit", "-m", msg)
	return env.Git(t, dir, "rev-parse", "HEAD")
}

func newSSOT(t *testing.T, env *testenv.Env) string {
	t.Helper()
	dir := filepath.Join(env.Root, "ssot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir ssot: %v", err)
	}
	env.Git(t, dir, "init", "-b", testenv.DefaultBranch)
	return dir
}

func TestFastForwardAdvancesBaseRef(t *testing.T) {
	env := testenv.New(t)
	dir := newSSOT(t, env)

	writeCommit(t, env, dir, "a.txt", "c0", "c0") // main = C0
	env.Git(t, dir, "branch", "target")           // target = C0
	env.Git(t, dir, "switch", "target")
	c1 := writeCommit(t, env, dir, "b.txt", "c1", "c1") // target = C1 (child of C0); main still C0
	env.Git(t, dir, "switch", "--detach")               // SSOT posture: no branch checked out

	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	if err := g.FastForwardBaseRef(ctx, dir, testenv.DefaultBranch, c1); err != nil {
		t.Fatalf("FastForwardBaseRef (true ff): %v", err)
	}
	got, err := g.ResolveRef(ctx, dir, "refs/heads/"+testenv.DefaultBranch)
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if got != c1 {
		t.Errorf("base ref = %s, want advanced to %s", got, c1)
	}
}

func TestFastForwardRefusesNonFastForward(t *testing.T) {
	env := testenv.New(t)
	dir := newSSOT(t, env)

	writeCommit(t, env, dir, "a.txt", "c0", "c0")       // main = C0
	env.Git(t, dir, "branch", "side")                   // side = C0
	c1 := writeCommit(t, env, dir, "b.txt", "c1", "c1") // main = C1
	env.Git(t, dir, "switch", "side")
	c2 := writeCommit(t, env, dir, "c.txt", "c2", "c2") // side = C2 (child of C0, sibling of C1)
	env.Git(t, dir, "switch", "--detach")

	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	before, err := g.ResolveRef(ctx, dir, "refs/heads/"+testenv.DefaultBranch)
	if err != nil {
		t.Fatalf("ResolveRef before: %v", err)
	}
	if before != c1 {
		t.Fatalf("precondition: base ref = %s, want %s", before, c1)
	}

	// C1..C2 is not a fast-forward (C1 is not an ancestor of C2): must be refused.
	err = g.FastForwardBaseRef(ctx, dir, testenv.DefaultBranch, c2)
	var nf *git.NonFastForwardError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v (%T), want *git.NonFastForwardError", err, err)
	}

	after, err := g.ResolveRef(ctx, dir, "refs/heads/"+testenv.DefaultBranch)
	if err != nil {
		t.Fatalf("ResolveRef after: %v", err)
	}
	if after != before {
		t.Errorf("base ref moved to %s on a refused non-ff; must stay %s", after, before)
	}
}
