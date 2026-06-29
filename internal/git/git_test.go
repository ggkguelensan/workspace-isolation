package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// pushChildToOrigin clones origin into a throwaway workdir, commits one child of
// the current base tip, and pushes it back — advancing origin's base by exactly
// one commit. It returns the pushed commit's SHA.
func pushChildToOrigin(t *testing.T, env *testenv.Env, origin string) string {
	t.Helper()
	work := filepath.Join(env.Root, "pusher")
	env.Git(t, env.Root, "clone", origin, work)
	c1 := writeCommit(t, env, work, "feature.txt", "c1", "c1")
	env.Git(t, work, "push", "origin", testenv.DefaultBranch)
	return c1
}

// fetchedMirror builds the canonical "origin advanced under us" scenario: a
// hermetic origin at C0, an EnsureClone'd SSOT mirror (local base + tracking ref
// both at C0), then a child commit C1 pushed to origin and Fetch'd into the
// mirror's remote-tracking ref. The local base is intentionally left at C0. It
// returns the mirror dir, the local base tip c0, and the pushed origin tip c1.
func fetchedMirror(t *testing.T, env *testenv.Env, g *git.Git, ctx context.Context) (dir, c0, c1 string) {
	t.Helper()
	origin := env.SeedOrigin(t, "acme")
	dir = filepath.Join(env.Root, "ssot")
	if err := g.EnsureClone(ctx, dir, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	var err error
	if c0, err = g.ResolveRef(ctx, dir, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("ResolveRef local base: %v", err)
	}
	c1 = pushChildToOrigin(t, env, origin)
	if err := g.Fetch(ctx, dir, "origin"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return dir, c0, c1
}

// Guard GIT-FETCH — the one network dial on the SSOT mirror (DESIGN §5, §2 #3).
//
// Fetch updates the mirror's remote-tracking refs from origin over the network —
// the only verb besides clone permitted to dial, so it routes through
// gitexec.RunNetwork. It must advance refs/remotes/origin/<base> to origin's new
// tip WITHOUT moving the local base ref or dirtying the working tree: advancing
// refs/heads/<base> is FastForwardBaseRef's exclusive job on the sync path, and
// the SSOT clone must stay pristine (DESIGN §5).
//
// Non-vacuity: make Fetch a no-op (return nil without running git fetch) → the
// remote-tracking ref stays at C0 → TestFetchAdvancesRemoteTrackingOnly RED.

func TestFetchAdvancesRemoteTrackingOnly(t *testing.T) {
	env := testenv.New(t)
	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	dir, c0, c1 := fetchedMirror(t, env, g, ctx)
	if c0 == c1 {
		t.Fatalf("precondition: pushed child %s equals base tip %s", c1, c0)
	}

	// The remote-tracking ref now sees origin's advanced tip...
	tracking, err := g.ResolveRef(ctx, dir, "refs/remotes/origin/"+testenv.DefaultBranch)
	if err != nil {
		t.Fatalf("ResolveRef remote-tracking: %v", err)
	}
	if tracking != c1 {
		t.Errorf("refs/remotes/origin/%s = %s, want fetched tip %s", testenv.DefaultBranch, tracking, c1)
	}

	// ...but the local base ref has NOT moved (only FastForwardBaseRef advances it)...
	localBase, err := g.ResolveRef(ctx, dir, "refs/heads/"+testenv.DefaultBranch)
	if err != nil {
		t.Fatalf("ResolveRef local base: %v", err)
	}
	if localBase != c0 {
		t.Errorf("local base ref = %s, want it left at %s (fetch must not advance the base)", localBase, c0)
	}

	// ...and the SSOT working tree stays pristine.
	clean, err := g.IsClean(ctx, dir)
	if err != nil {
		t.Fatalf("IsClean: %v", err)
	}
	if !clean {
		t.Errorf("SSOT mirror dirty after fetch; it must stay pristine")
	}
}

// Guard GIT-DIVERGED — ahead/behind counts from LOCAL refs only (offline).
//
// DivergedCounts powers both freshness (behind) and main_state classification
// (ahead/behind/diverged). It reads `rev-list --left-right --count
// local...remote`: the left column counts how many commits local is AHEAD of
// remote, the right how many it is BEHIND. After fetching a one-ahead origin, the
// local base is behind by exactly 1 / ahead 0; reversing the args flips it to
// ahead 1 / behind 0 — pinning that each count is read from the correct column.
//
// Non-vacuity: swap the two columns (read ahead from the behind field and vice
// versa) → both orientations' assertions RED.

func TestDivergedCountsAheadBehind(t *testing.T) {
	env := testenv.New(t)
	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	dir, _, _ := fetchedMirror(t, env, g, ctx)
	local := "refs/heads/" + testenv.DefaultBranch
	remote := "refs/remotes/origin/" + testenv.DefaultBranch

	ahead, behind, err := g.DivergedCounts(ctx, dir, local, remote)
	if err != nil {
		t.Fatalf("DivergedCounts(local, remote): %v", err)
	}
	if ahead != 0 || behind != 1 {
		t.Errorf("local vs origin: ahead=%d behind=%d, want ahead=0 behind=1", ahead, behind)
	}

	ahead, behind, err = g.DivergedCounts(ctx, dir, remote, local)
	if err != nil {
		t.Fatalf("DivergedCounts(remote, local): %v", err)
	}
	if ahead != 1 || behind != 0 {
		t.Errorf("origin vs local: ahead=%d behind=%d, want ahead=1 behind=0", ahead, behind)
	}
}

// Guard GIT-WORKTREE — per-repo isolate materialization (DESIGN §1, §5, §7.1).
//
// AddWorktree is the primitive isolate new composes once per declared repo. It
// must materialize a worktree that is, all at once:
//
//	(1) DETACHED at the requested rev — it holds no branch, so the SSOT base ref
//	    is never "checked out in a worktree" and FastForwardBaseRef can always
//	    advance it (the keystone, DESIGN §5);
//	(2) a LINKED worktree SHARING the SSOT object store — its .git is a gitlink
//	    *file* and its common git dir resolves to the SSOT's .git, so there is no
//	    object duplication (the isolation invariant, DESIGN §1 line 30);
//	(3) materialized WITHOUT dirtying the SSOT working tree (it must stay
//	    pristine — DESIGN §5).
//
// Non-vacuity: materialize via a standalone `git clone` instead of a linked
// worktree → the result checks out the base BRANCH (abbrev-ref "main", not
// "HEAD") and has its OWN .git directory + object store (common git dir is the
// clone's, not the SSOT's) → all three assertions RED. This is the faithful
// mutant because worktree-vs-clone is precisely the design choice the guard
// protects (native object-store sharing, no duplication — DESIGN §1 line 30),
// not merely "a checkout appeared". (--detach is defense-in-depth: a SHA or
// fully-qualified ref like refs/heads/<base> already detaches, but a caller that
// passes a short branch name still gets a detached worktree.)

func TestAddWorktreeIsDetachedLinkedAndShared(t *testing.T) {
	env := testenv.New(t)
	origin := env.SeedOrigin(t, "acme")
	ssot := filepath.Join(env.Root, "ssot")

	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	if err := g.EnsureClone(ctx, ssot, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	baseRef := "refs/heads/" + testenv.DefaultBranch
	baseTip := env.Git(t, ssot, "rev-parse", baseRef)

	// isolas/<task>/<repo> — the per-isolate worktree path (layout owns this in the
	// real flow; here we just place it under the hermetic root).
	wt := filepath.Join(env.Root, "isolas", "taskx", "acme")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatalf("mkdir isolate dir: %v", err)
	}

	if err := g.AddWorktree(ctx, ssot, wt, baseRef); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// (1) detached at the base tip — no branch checked out in the worktree.
	if got := env.Git(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("worktree HEAD abbrev-ref = %q, want %q (detached, no branch)", got, "HEAD")
	}
	if got := env.Git(t, wt, "rev-parse", "HEAD"); got != baseTip {
		t.Errorf("worktree HEAD = %s, want the base tip %s", got, baseTip)
	}

	// (2) linked worktree sharing the SSOT object store: .git is a gitlink FILE,
	// and the common git dir resolves to the SSOT's own .git (no object dup).
	fi, err := os.Stat(filepath.Join(wt, ".git"))
	if err != nil {
		t.Fatalf("stat worktree .git: %v", err)
	}
	if fi.IsDir() {
		t.Errorf("worktree .git is a directory; a linked worktree's .git must be a gitlink file")
	}
	commonDir := env.Git(t, wt, "rev-parse", "--path-format=absolute", "--git-common-dir")
	ssotGitDir := env.Git(t, ssot, "rev-parse", "--path-format=absolute", "--git-dir")
	if commonDir != ssotGitDir {
		t.Errorf("worktree common git dir = %q, want the SSOT's git dir %q (shared store, no dup)", commonDir, ssotGitDir)
	}

	// (3) the SSOT working tree stays pristine after the worktree add.
	clean, err := g.IsClean(ctx, ssot)
	if err != nil {
		t.Fatalf("IsClean ssot: %v", err)
	}
	if !clean {
		t.Errorf("SSOT dirty after worktree add; it must stay pristine")
	}
}

// Guard GIT-OWNED-REF — evidence-positive ownership marker (DESIGN §7.1, decision #2).
//
// CreateOwnedRef records that wi created the (task, repo) worktree by writing the
// marker ref refs/wi/owned/<task>/<repo> at the worktree's sha; OwnedRefSHA reads
// it back. This marker is the POSITIVE evidence reclamation requires (DESIGN §7.1):
// a worktree or branch is reclaimable only if such a ref proves wi owns it — an
// unexplained orphan with no marker is a HARD BLOCK, never auto-pruned.
//
// Two load-bearing properties (decision #2 — a git ref chosen over a note/reflog):
//
//	(1) the marker lives under refs/wi/*, NOT refs/heads/* — so its commit stays
//	    gc-reachable (a ref protects its object) yet it is NOT a branch, so it never
//	    appears as a stray branch violating the SSOT-pristine invariant (DESIGN §5);
//	(2) creation is atomic (a single update-ref) and the read cleanly distinguishes
//	    a genuinely absent marker (exists=false, nil error) from a real read error.
//
// Non-vacuity: create the marker under refs/heads/ instead of refs/wi/ (flip the
// namespace in ownedRef) → the marker becomes a stray branch: refs/wi/owned/ is
// empty while refs/heads/ grows a second ref → both the "lives under refs/wi at the
// sha" and the "no stray branch" assertions RED. This is the faithful mutant because
// the refs/wi-vs-refs/heads namespace IS precisely what decision #2 buys (gc-
// protection without leaking a branch). (A no-op CreateOwnedRef additionally reddens
// the round-trip absent→present, proving the verb does real work.)

func TestOwnedRefMarksOwnershipUnderRefsWi(t *testing.T) {
	env := testenv.New(t)
	origin := env.SeedOrigin(t, "acme")
	ssot := filepath.Join(env.Root, "ssot")

	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	if err := g.EnsureClone(ctx, ssot, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	baseTip := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
	const task, repo = "taskx", "acme"

	// Absent before: the read verb cleanly reports "no marker recorded", not an error
	// (the common case reclamation inspects on an orphan).
	if sha, exists, err := g.OwnedRefSHA(ctx, ssot, task, repo); err != nil || exists {
		t.Fatalf("OwnedRefSHA before create = (%q, %v, %v), want (\"\", false, nil)", sha, exists, err)
	}

	if err := g.CreateOwnedRef(ctx, ssot, task, repo, baseTip); err != nil {
		t.Fatalf("CreateOwnedRef: %v", err)
	}

	// Present after, at the recorded sha — read back through the verb...
	sha, exists, err := g.OwnedRefSHA(ctx, ssot, task, repo)
	if err != nil {
		t.Fatalf("OwnedRefSHA after create: %v", err)
	}
	if !exists || sha != baseTip {
		t.Errorf("OwnedRefSHA after create = (%q, %v), want (%q, true)", sha, exists, baseTip)
	}

	// ...and the marker really lives under refs/wi/owned/ at that sha (verified with
	// raw git, not the verb under test).
	rawOwned := env.Git(t, ssot, "for-each-ref", "--format=%(objectname)", "refs/wi/owned/"+task+"/"+repo)
	if rawOwned != baseTip {
		t.Errorf("refs/wi/owned/%s/%s = %q, want the worktree sha %q", task, repo, rawOwned, baseTip)
	}

	// The marker is NOT a branch: refs/heads/ still holds only the base ref, so the
	// SSOT never grows a stray branch — the marker is gc-protected via refs/wi/* alone.
	branches := env.Git(t, ssot, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if want := "refs/heads/" + testenv.DefaultBranch; branches != want {
		t.Errorf("refs/heads/ = %q, want only %q (marker must not leak a branch)", branches, want)
	}
}

// Guard GIT-RECLAIM — evidence-positive worktree reclamation primitives (DESIGN §7.1, §7.2).
//
// `isolate rm` reclaims a worktree ONLY after proving wi owns it (the marker ref,
// guard GIT-OWNED-REF) AND it is clean AND not ahead of base. The two git-level
// operations it then composes are:
//
//	RemoveWorktree  — `git worktree remove <path>`: it deletes the worktree directory
//	                  AND DEREGISTERS it from the SSOT's worktree admin
//	                  (.git/worktrees/<id>), unlike a bare directory delete that would
//	                  strand a stale admin entry. It uses NO --force and performs NO
//	                  `git reset --hard` (DESIGN §7.2): a worktree carrying modified or
//	                  untracked files is REFUSED and left intact — a second safety net
//	                  beneath the isolate layer's own cleanliness gate.
//	DeleteOwnedRef  — clears the ownership marker refs/wi/owned/<task>/<repo> with a
//	                  single `update-ref -d`, once the worktree it vouched for is gone.
//	                  Deleting an already-absent marker is a no-op success, so a re-run
//	                  of reclamation stays idempotent.
//
// Non-vacuity (RemoveWorktree): replace `git worktree remove` with a bare
// os.RemoveAll(worktreePath) → the directory vanishes but the SSOT's worktree admin
// entry survives → TestRemoveWorktreeDeregisters RED (`git worktree list` still names
// the removed path). A second mutant — add --force — reddens TestRemoveWorktreeRefusesDirty
// by nuking a dirty worktree the no-force discipline must protect.
// Non-vacuity (DeleteOwnedRef): make it a no-op → the marker survives →
// TestDeleteOwnedRefClearsMarker RED on the absent-after assertion.

// addCleanWorktree EnsureClone's an SSOT off a freshly seeded origin and adds one
// detached worktree off the base tip, returning the ssot dir and the worktree path.
func addCleanWorktree(t *testing.T, env *testenv.Env, g *git.Git, ctx context.Context) (ssot, wt string) {
	t.Helper()
	origin := env.SeedOrigin(t, "acme")
	ssot = filepath.Join(env.Root, "ssot")
	if err := g.EnsureClone(ctx, ssot, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	wt = filepath.Join(env.Root, "isolas", "taskx", "acme")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatalf("mkdir isolate dir: %v", err)
	}
	if err := g.AddWorktree(ctx, ssot, wt, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if list := env.Git(t, ssot, "worktree", "list", "--porcelain"); !strings.Contains(list, wt) {
		t.Fatalf("precondition: worktree %s not registered:\n%s", wt, list)
	}
	return ssot, wt
}

func TestRemoveWorktreeDeregisters(t *testing.T) {
	env := testenv.New(t)
	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	ssot, wt := addCleanWorktree(t, env, g, ctx)

	if err := g.RemoveWorktree(ctx, ssot, wt); err != nil {
		t.Fatalf("RemoveWorktree (clean): %v", err)
	}

	// The worktree directory is gone from disk...
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present after RemoveWorktree (stat err = %v)", err)
	}
	// ...AND the SSOT no longer registers it — proper deregistration, not a bare
	// rm -rf that would strand a .git/worktrees admin entry.
	if list := env.Git(t, ssot, "worktree", "list", "--porcelain"); strings.Contains(list, wt) {
		t.Errorf("worktree %s still registered after RemoveWorktree:\n%s", wt, list)
	}
	// The SSOT working tree stays pristine throughout.
	clean, err := g.IsClean(ctx, ssot)
	if err != nil {
		t.Fatalf("IsClean ssot: %v", err)
	}
	if !clean {
		t.Errorf("SSOT dirty after worktree remove; it must stay pristine")
	}
}

func TestRemoveWorktreeRefusesDirty(t *testing.T) {
	env := testenv.New(t)
	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	ssot, wt := addCleanWorktree(t, env, g, ctx)

	// Dirty the worktree with an untracked file — the unclean state DESIGN §7.1
	// forbids reclaiming. No --force, no reset --hard: the remove must REFUSE and
	// leave the worktree intact.
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	if err := g.RemoveWorktree(ctx, ssot, wt); err == nil {
		t.Fatalf("RemoveWorktree removed a dirty worktree; want a refusal error")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("dirty worktree gone after a refused remove (stat err = %v); it must be left intact", err)
	}
	if list := env.Git(t, ssot, "worktree", "list", "--porcelain"); !strings.Contains(list, wt) {
		t.Errorf("dirty worktree deregistered after a refused remove; it must stay registered:\n%s", list)
	}
}

func TestDeleteOwnedRefClearsMarker(t *testing.T) {
	env := testenv.New(t)
	origin := env.SeedOrigin(t, "acme")
	ssot := filepath.Join(env.Root, "ssot")

	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	if err := g.EnsureClone(ctx, ssot, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	baseTip := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
	const task, repo = "taskx", "acme"

	if err := g.CreateOwnedRef(ctx, ssot, task, repo, baseTip); err != nil {
		t.Fatalf("CreateOwnedRef: %v", err)
	}
	if _, exists, err := g.OwnedRefSHA(ctx, ssot, task, repo); err != nil || !exists {
		t.Fatalf("precondition: marker should exist, got (exists=%v, err=%v)", exists, err)
	}

	if err := g.DeleteOwnedRef(ctx, ssot, task, repo); err != nil {
		t.Fatalf("DeleteOwnedRef: %v", err)
	}
	// The marker is gone — read back through the verb (absent, not an error).
	if sha, exists, err := g.OwnedRefSHA(ctx, ssot, task, repo); err != nil || exists {
		t.Errorf("OwnedRefSHA after delete = (%q, %v, %v), want (\"\", false, nil)", sha, exists, err)
	}
	// ...and confirmed with raw git: refs/wi/owned/ holds nothing for this pair.
	if raw := env.Git(t, ssot, "for-each-ref", "--format=%(refname)", "refs/wi/owned/"+task+"/"+repo); raw != "" {
		t.Errorf("refs/wi/owned/%s/%s still present after delete: %q", task, repo, raw)
	}
	// Idempotent: deleting an already-absent marker is a no-op success, so a re-run
	// of reclamation does not error.
	if err := g.DeleteOwnedRef(ctx, ssot, task, repo); err != nil {
		t.Errorf("DeleteOwnedRef on an absent marker should be a no-op success, got %v", err)
	}
}
