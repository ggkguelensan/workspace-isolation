// Package git provides wi's deterministic, typed git verbs on top of
// internal/gitexec (DESIGN §4). It contains no path or policy logic — just the
// thin, well-defined git operations the domain packages compose.
//
// Its keystone is FastForwardBaseRef, the SOLE base-ref-mutation path in the
// entire codebase (DESIGN §5). The SSOT clone is kept on a detached HEAD at the
// base tip, and both v0 sync and v1 land advance refs/heads/<base> through this
// one function — fast-forward-only, via update-ref, with no checkout and no
// merge — so the SSOT base is append-only and the two commands coexist on one
// clone with zero rework.
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
)

// Git runs typed git verbs through a gitexec.Runner. Almost every verb is local
// and uses the offline Runner.Run path; the two network-permitted verbs —
// EnsureClone and Fetch — are the sole verbs that route through RunNetwork and
// dial (DESIGN §2 #3, §5).
type Git struct {
	r *gitexec.Runner
}

// New returns a Git backed by r.
func New(r *gitexec.Runner) *Git { return &Git{r: r} }

// ResolveRef returns the commit SHA that ref resolves to in the repo at dir,
// verifying it exists. ref is taken literally (e.g. "HEAD", a full SHA, or
// "refs/heads/main").
func (g *Git) ResolveRef(ctx context.Context, dir, ref string) (string, error) {
	res, err := g.r.Run(ctx, dir, "rev-parse", "--verify", "--end-of-options", ref)
	if err != nil {
		return "", fmt.Errorf("git: resolve ref %q in %s: %w", ref, dir, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// EnsureClone lazily materializes the SSOT clone for originURL at dir, left in
// wi's SSOT posture: a detached HEAD at refs/heads/<base>'s tip (DESIGN §5).
// The base branch ref is created (it is the ref FastForwardBaseRef advances)
// but is NOT left checked out, so later ref advances never disturb a working
// tree. If dir is already a git repo, EnsureClone is a noop — it never
// re-clones and performs no network I/O. Cloning is the one network-permitted
// verb in wi, so it (and only it) routes through gitexec.RunNetwork; the detach
// step is local.
func (g *Git) EnsureClone(ctx context.Context, dir, originURL, base string) error {
	if g.isRepo(ctx, dir) {
		return nil
	}
	// Clone only the base branch, so refs/heads/<base> exists locally at the
	// origin's base tip. The clone creates dir, so it runs from the current
	// working directory (empty cmd dir) with an explicit target.
	if _, err := g.r.RunNetwork(ctx, "", "clone", "--branch", base, "--", originURL, dir); err != nil {
		return fmt.Errorf("git: clone %s (branch %s) into %s: %w", originURL, base, dir, err)
	}
	// Detach HEAD at the freshly checked-out base tip so no branch is checked
	// out; refs/heads/<base> remains in place for FastForwardBaseRef to advance.
	if _, err := g.r.Run(ctx, dir, "switch", "--detach"); err != nil {
		return fmt.Errorf("git: detach HEAD in %s: %w", dir, err)
	}
	return nil
}

// Fetch updates dir's remote-tracking refs (refs/remotes/<remote>/*) from the
// network. With clone it is one of only two network-permitted verbs in wi, so it
// routes through gitexec.RunNetwork. Fetch never moves a local branch ref and
// never touches the working tree — advancing the SSOT base is FastForwardBaseRef's
// exclusive job on the sync path (DESIGN §5). remote is wi-internal (always
// "origin"), not user input.
func (g *Git) Fetch(ctx context.Context, dir, remote string) error {
	if _, err := g.r.RunNetwork(ctx, dir, "fetch", "--end-of-options", remote); err != nil {
		return fmt.Errorf("git: fetch %s in %s: %w", remote, dir, err)
	}
	return nil
}

// DivergedCounts reports how many commits local is ahead of and behind remote,
// computed from LOCAL refs only (no network). It is the basis for both freshness
// (behind = how far the base trails origin as of the last fetch) and main_state
// classification (ahead/behind/diverged). Both refs must resolve; the counts come
// from `rev-list --left-right --count local...remote`, whose left column is the
// ahead count and right column the behind count.
func (g *Git) DivergedCounts(ctx context.Context, dir, local, remote string) (ahead, behind int, err error) {
	res, err := g.r.Run(ctx, dir, "rev-list", "--left-right", "--count", "--end-of-options", local+"..."+remote)
	if err != nil {
		return 0, 0, fmt.Errorf("git: diverged counts %s...%s in %s: %w", local, remote, dir, err)
	}
	fields := strings.Fields(res.Stdout)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("git: unexpected rev-list output %q for %s...%s in %s", res.Stdout, local, remote, dir)
	}
	if ahead, err = strconv.Atoi(fields[0]); err != nil {
		return 0, 0, fmt.Errorf("git: parse ahead count %q: %w", fields[0], err)
	}
	if behind, err = strconv.Atoi(fields[1]); err != nil {
		return 0, 0, fmt.Errorf("git: parse behind count %q: %w", fields[1], err)
	}
	return ahead, behind, nil
}

// AddWorktree materializes a linked git worktree at worktreePath off the SSOT
// clone at ssotDir, checked out DETACHED at rev. It is the per-repo isolate
// materialization primitive isolate new composes (DESIGN §1, §6.3).
//
// Two properties are load-bearing. The worktree shares the SSOT's object store —
// native git worktree sharing, so no objects are duplicated (DESIGN §1 line 30):
// the worktree's .git is a gitlink file into <ssotDir>/.git/worktrees/<id>. And
// it is DETACHED (--detach forces this even when rev names a branch), so it holds
// no branch ref; the SSOT base ref is therefore never "checked out in a worktree"
// and FastForwardBaseRef can always advance it (the keystone, DESIGN §5). It is a
// local operation (offline Run). rev is wi-internal — a SHA or a ref such as
// refs/heads/<base>. Ownership/gc-protection via the refs/wi/owned/<task>/<repo>
// marker (DESIGN §7.1) is layered on by a separate step, not here.
func (g *Git) AddWorktree(ctx context.Context, ssotDir, worktreePath, rev string) error {
	if _, err := g.r.Run(ctx, ssotDir, "worktree", "add", "--detach", worktreePath, rev); err != nil {
		return fmt.Errorf("git: add worktree %s at %s in %s: %w", worktreePath, rev, ssotDir, err)
	}
	return nil
}

// PruneWorktrees deregisters stale linked-worktree admin entries from the SSOT at
// ssotDir with `git worktree prune` — the entries left behind when a worktree's
// directory was removed out-of-band (an external `rm -rf`, a crash mid-materialize)
// instead of via `git worktree remove`. Such a stale entry makes its path "missing but
// already registered", which would make `git worktree add` refuse a re-add; pruning it
// clears the path for re-materialization. It deregisters ONLY entries whose working
// directory is genuinely missing — git never prunes a live worktree — so it can be run
// safely before the HEAL-1 reconciler re-adds a MissingWorktree cell at its marker sha.
// It is a local operation (offline Run) and is idempotent (a no-op when nothing is
// stale).
func (g *Git) PruneWorktrees(ctx context.Context, ssotDir string) error {
	if _, err := g.r.Run(ctx, ssotDir, "worktree", "prune"); err != nil {
		return fmt.Errorf("git: prune worktrees in %s: %w", ssotDir, err)
	}
	return nil
}

// ownedRef is the wi-owned marker ref for (task, repo). Like FastForwardBaseRef's
// "refs/heads/"+base, the namespace is wi convention encoded in exactly one place:
// markers live under refs/wi/owned/ — a ref (so its commit stays gc-reachable) that
// is NOT a branch (so it never appears as a stray branch in the pristine SSOT,
// DESIGN §5).
func ownedRef(task, repo string) string {
	return "refs/wi/owned/" + task + "/" + repo
}

// CreateOwnedRef records wi's ownership of the (task, repo) worktree by atomically
// creating the marker ref refs/wi/owned/<task>/<repo> at sha (a single update-ref).
// This is the POSITIVE evidence reclamation requires (DESIGN §7.1, decision #2): a
// worktree or branch is reclaimable only if such a marker proves wi created it; an
// unexplained orphan with no marker is a hard block, never auto-pruned. A git ref
// is chosen over a note/reflog precisely because it gives atomic creation and gc-
// protection (the ref keeps its commit reachable) while staying out of the branch
// namespace. It is a local operation. task/repo are wi-internal and already
// segment-validated by the caller before they reach here — this package holds no
// path policy, exactly as base is the caller's concern in FastForwardBaseRef.
func (g *Git) CreateOwnedRef(ctx context.Context, ssotDir, task, repo, sha string) error {
	ref := ownedRef(task, repo)
	if _, err := g.r.Run(ctx, ssotDir, "update-ref", ref, sha); err != nil {
		return fmt.Errorf("git: create owned ref %s -> %s in %s: %w", ref, sha, ssotDir, err)
	}
	return nil
}

// OwnedRefSHA reports the sha the marker ref refs/wi/owned/<task>/<repo> points at
// and whether it exists, cleanly distinguishing a genuinely absent marker
// (exists=false, nil error — the "no ownership recorded" case reclamation inspects
// on an orphan) from a real read failure. It is a local, read-only operation:
// `rev-parse --verify --quiet` emits the sha and exits 0 when the ref resolves, and
// exits 1 with no output for a valid-but-absent ref.
func (g *Git) OwnedRefSHA(ctx context.Context, ssotDir, task, repo string) (sha string, exists bool, err error) {
	ref := ownedRef(task, repo)
	res, runErr := g.r.Run(ctx, ssotDir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	if runErr != nil {
		var ee *gitexec.ExitError
		if errors.As(runErr, &ee) && ee.Result.ExitCode == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("git: read owned ref %s in %s: %w", ref, ssotDir, runErr)
	}
	return strings.TrimSpace(res.Stdout), true, nil
}

// RemoveWorktree removes the linked worktree at worktreePath from the SSOT at
// ssotDir with `git worktree remove`, which deletes the worktree directory AND
// deregisters it from the SSOT's worktree admin (.git/worktrees/<id>) — unlike a
// bare directory delete, which would strand a stale admin entry. It is the
// reclamation verb `isolate rm` composes AFTER proving wi owns the worktree and it
// is clean + not ahead of base (DESIGN §7.1). It passes NO --force and performs NO
// `git reset --hard` (DESIGN §7.2): a worktree carrying modified or untracked files
// is REFUSED and left intact, a second safety net beneath the isolate layer's own
// cleanliness gate. It is a local operation (offline Run).
func (g *Git) RemoveWorktree(ctx context.Context, ssotDir, worktreePath string) error {
	if _, err := g.r.Run(ctx, ssotDir, "worktree", "remove", worktreePath); err != nil {
		return fmt.Errorf("git: remove worktree %s in %s: %w", worktreePath, ssotDir, err)
	}
	return nil
}

// DeleteOwnedRef clears the ownership marker refs/wi/owned/<task>/<repo> with a
// single `update-ref -d`, called once the worktree the marker vouched for has been
// reclaimed (its evidence-positive job is done — DESIGN §7.1). It is a local
// operation. Deleting an already-absent marker is a no-op success (git's update-ref
// -d with no expected old value succeeds on a missing ref), so a re-run of
// reclamation stays idempotent. task/repo are wi-internal and already segment-
// validated by the caller, exactly as in CreateOwnedRef.
func (g *Git) DeleteOwnedRef(ctx context.Context, ssotDir, task, repo string) error {
	ref := ownedRef(task, repo)
	if _, err := g.r.Run(ctx, ssotDir, "update-ref", "-d", ref); err != nil {
		return fmt.Errorf("git: delete owned ref %s in %s: %w", ref, ssotDir, err)
	}
	return nil
}

// isRepo reports whether dir is an existing git repository. It guards the dir's
// existence first so git is never spawned in a missing directory (which would
// be an opaque start failure rather than a clean "not a repo").
func (g *Git) isRepo(ctx context.Context, dir string) bool {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return false
	}
	_, err := g.r.Run(ctx, dir, "rev-parse", "--git-dir")
	return err == nil
}

// StatusPorcelain returns `git status --porcelain` output for dir (machine
// format, stable across git versions). Empty output means a pristine tree.
func (g *Git) StatusPorcelain(ctx context.Context, dir string) (string, error) {
	res, err := g.r.Run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git: status %s: %w", dir, err)
	}
	return res.Stdout, nil
}

// IsClean reports whether dir's working tree and index are completely
// unmodified, including the absence of untracked files. This is the
// SSOT-pristine check (DESIGN §5): any drift at all is not clean.
func (g *Git) IsClean(ctx context.Context, dir string) (bool, error) {
	out, err := g.StatusPorcelain(ctx, dir)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// NonFastForwardError reports that advancing Base to New would not be a
// fast-forward — Current (the present base tip) is not an ancestor of New — so
// the base ref was left unchanged. This is the safety check that keeps the SSOT
// base append-only (it also rejects rewinds).
type NonFastForwardError struct {
	Base    string // base branch name (e.g. "main")
	Current string // SHA the base ref currently points at
	New     string // SHA the caller asked to advance to
}

func (e *NonFastForwardError) Error() string {
	return fmt.Sprintf("git: refusing non-fast-forward update of %s: %s is not an ancestor of %s",
		e.Base, e.Current, e.New)
}

// FastForwardBaseRef advances refs/heads/<base> in the repo at dir to newRev,
// but ONLY if doing so is a fast-forward (the current base tip is an ancestor of
// newRev). It performs no checkout and no merge, so it operates on the detached
// SSOT clone; on a non-fast-forward it returns *NonFastForwardError and leaves
// the ref untouched. This is the only function in wi permitted to move a base
// ref (DESIGN §5).
func (g *Git) FastForwardBaseRef(ctx context.Context, dir, base, newRev string) error {
	baseRef := "refs/heads/" + base
	current, err := g.ResolveRef(ctx, dir, baseRef)
	if err != nil {
		return err
	}
	newSHA, err := g.ResolveRef(ctx, dir, newRev)
	if err != nil {
		return err
	}

	// Fast-forward safety: current must be an ancestor of newSHA. merge-base
	// --is-ancestor exits 0 when true, 1 when false (a genuine non-ff), and other
	// codes on error.
	if _, ffErr := g.r.Run(ctx, dir, "merge-base", "--is-ancestor", current, newSHA); ffErr != nil {
		var ee *gitexec.ExitError
		if errors.As(ffErr, &ee) && ee.Result.ExitCode == 1 {
			return &NonFastForwardError{Base: base, Current: current, New: newSHA}
		}
		return fmt.Errorf("git: ancestry check %s..%s in %s: %w", current, newSHA, dir, ffErr)
	}

	// Move the ref with the old value asserted, so a concurrent change between the
	// ancestry check and here makes update-ref fail atomically rather than racing.
	if _, err := g.r.Run(ctx, dir, "update-ref", baseRef, newSHA, current); err != nil {
		return fmt.Errorf("git: update-ref %s -> %s in %s: %w", baseRef, newSHA, dir, err)
	}
	return nil
}
