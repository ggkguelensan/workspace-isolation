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
