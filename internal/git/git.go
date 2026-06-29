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
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
)

// Git runs typed git verbs through a gitexec.Runner. All verbs here are local
// (no network), so they use the offline Runner.Run path and never dial.
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
