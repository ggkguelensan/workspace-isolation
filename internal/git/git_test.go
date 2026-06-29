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

// Guard GIT-CLONE-DETACHED — the SSOT clone lifecycle (DESIGN §5).
//
// EnsureClone lazily materializes an absent SSOT clone and leaves it in the
// SSOT posture: a DETACHED HEAD sitting at refs/heads/<base>'s tip, with the
// base branch ref present (it is the ref FastForwardBaseRef advances) but NOT
// checked out, so advancing the ref via update-ref never disturbs a working
// tree. Cloning is the one network-permitted verb, so it routes through
// gitexec.RunNetwork; a second call on an existing clone is a noop — no
// re-clone, no network.
//
// Non-vacuity: clone without detaching (leave <base> checked out) → HEAD's
// abbrev-ref is the branch name, not "HEAD" → TestEnsureCloneDetachesAtBaseTip
// RED.

func TestEnsureCloneDetachesAtBaseTip(t *testing.T) {
	env := testenv.New(t)
	origin := env.SeedOrigin(t, "acme")
	originURL := "file://" + origin
	dir := filepath.Join(env.Root, "ssot")

	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	if err := g.EnsureClone(ctx, dir, originURL, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}

	// SSOT posture: HEAD is detached (no branch is checked out). git reports the
	// literal "HEAD" for the abbrev-ref of a detached HEAD.
	if got := env.Git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("HEAD abbrev-ref = %q, want %q (detached, no branch checked out)", got, "HEAD")
	}
	// ...but it sits exactly at the base ref's tip, and the base ref exists (it
	// is what FastForwardBaseRef later advances).
	baseTip := env.Git(t, dir, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
	headSHA := env.Git(t, dir, "rev-parse", "HEAD")
	if headSHA != baseTip {
		t.Errorf("HEAD = %s, want it detached at the base tip %s", headSHA, baseTip)
	}
}

func TestEnsureCloneIsIdempotent(t *testing.T) {
	env := testenv.New(t)
	origin := env.SeedOrigin(t, "acme")
	originURL := "file://" + origin
	dir := filepath.Join(env.Root, "ssot")

	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	if err := g.EnsureClone(ctx, dir, originURL, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone (first): %v", err)
	}

	// A sentinel a re-clone could not preserve: git clone refuses a non-empty
	// target dir, and would never leave a stray file behind. Its survival proves
	// the second call was a true noop.
	sentinel := filepath.Join(dir, ".wi-sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	if err := g.EnsureClone(ctx, dir, originURL, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone (second, existing clone): %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel gone after the second EnsureClone (%v); it re-cloned instead of no-op", err)
	}
}

// Guard GIT-CLEAN — the SSOT-pristine predicate (DESIGN §5).
//
// IsClean reports whether dir's working tree and index are completely
// unmodified (porcelain status empty) — INCLUDING the absence of untracked
// files, since a stray turd in the SSOT clone is exactly the drift the pristine
// invariant forbids. Two-sided: a fresh clone is clean; any change (here, one
// untracked file) makes it dirty.
//
// Non-vacuity: make IsClean ignore the porcelain output and always return true
// → the dirtied case of TestIsCleanReflectsWorkingTree RED.

func TestIsCleanReflectsWorkingTree(t *testing.T) {
	env := testenv.New(t)
	origin := env.SeedOrigin(t, "acme")
	dir := filepath.Join(env.Root, "ssot")

	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	if err := g.EnsureClone(ctx, dir, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}

	clean, err := g.IsClean(ctx, dir)
	if err != nil {
		t.Fatalf("IsClean (fresh): %v", err)
	}
	if !clean {
		t.Errorf("fresh clone IsClean = false, want true (pristine)")
	}

	if err := os.WriteFile(filepath.Join(dir, "turd.txt"), []byte("dirt"), 0o644); err != nil {
		t.Fatalf("write turd: %v", err)
	}
	clean, err = g.IsClean(ctx, dir)
	if err != nil {
		t.Fatalf("IsClean (dirty): %v", err)
	}
	if clean {
		t.Errorf("clone with an untracked file IsClean = true, want false (not pristine)")
	}
}
