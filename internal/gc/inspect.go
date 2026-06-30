package gc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Inspect is the read-only data-gathering half of `wi gc` (HEAL-2, DESIGN §7.1/§7.5):
// the workspace-wide sweep that observes every wi worktree and its four reclamation
// signals, leaving the verdict to Classify and the action to Collect. Like
// isolate.Inspect and lock.List it takes no lock, mutates nothing, and dials no
// network — every read is a local ref read, an os.Stat, or a `git status`.
//
// CANDIDATE POPULATION = the on-disk isolate worktrees under <root>/isolas/<task>/<repo>
// (decision recorded in PROGRESS.md). This is deliberately the WORKTREE axis, not the
// marker axis, and it is what makes the §7.5 orphan inventory structural: a worktree wi
// cannot prove it owns (no marker) SURFACES here as ClassOrphanUnexplained precisely
// because the worktree — not the marker — is what we enumerate. The complementary case,
// a surviving marker whose worktree is GONE, is deliberately NOT gc's concern: that is
// HEAL-1's re-materialize axis (isolate.ClassMissingWorktree), keyed off the registry
// record. Keeping gc worktree-scoped and repair record-scoped stops the two heals from
// fighting over the same cell.
//
// The four signals per cell mirror the §7.1 reclamation conjunction exactly:
//   - HasMarker   — the wi-owned marker ref proves wi created the worktree (git.OwnedRefSHA).
//   - Live        — a live registry record still claims (task, repo) (built from state.List).
//   - Clean       — the worktree has no uncommitted changes (git.IsClean).
//   - AheadOfBase — the worktree HEAD has moved past the marker's recorded base sha.
//     This mirrors isolate.Remove's v0 convention (reclaimRepo gate 3): the marker IS the
//     base evidence, so HEAD != markerSHA means the worktree carries local commits. Only
//     meaningful when HasMarker — for a markerless orphan the marker gate short-circuits
//     the verdict before the work signals are consulted, so it is left false.
//
// A workspace with no isolas/ directory (no isolate ever created) yields the empty list,
// not an error — the same idempotent posture state.List and the reclamation verbs take.
// A genuine git/layout/registry fault (an unsafe path segment, a ref read that blew up, a
// torn registry record) is returned as an error: it is an environment failure, not a
// per-cell verdict, since every reclamation state is already expressible through the four
// signals.
func Inspect(ctx context.Context, l layout.Layout, g *git.Git) ([]Candidate, error) {
	live, err := liveSet(l)
	if err != nil {
		return nil, err
	}

	isolasDir := l.IsolasDir()
	taskEntries, err := os.ReadDir(isolasDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // no isolate ever created → nothing to reclaim
		}
		return nil, fmt.Errorf("gc: list isolate tasks in %s: %w", isolasDir, err)
	}

	var cands []Candidate
	for _, te := range taskEntries {
		if !te.IsDir() {
			continue
		}
		task := te.Name()
		taskDir := filepath.Join(isolasDir, task)
		repoEntries, err := os.ReadDir(taskDir)
		if err != nil {
			return nil, fmt.Errorf("gc: list isolate worktrees in %s: %w", taskDir, err)
		}
		for _, re := range repoEntries {
			if !re.IsDir() {
				continue
			}
			cand, err := observeCandidate(ctx, l, g, task, re.Name(), live)
			if err != nil {
				return nil, err
			}
			cands = append(cands, cand)
		}
	}
	return cands, nil
}

// cellKey identifies one (task, repo) cell for live-set membership.
type cellKey struct{ task, repo string }

// liveSet builds the set of cells a live registry record currently claims, from
// state.List (DESIGN §7.1: a cell journaled as live is never gc's to collect). A
// missing/empty stateDir yields the empty set (no live records); a torn registry entry
// is surfaced as state.List's error, never silently treated as "not live".
func liveSet(l layout.Layout) (map[cellKey]bool, error) {
	recs, err := state.List(l.StateDir())
	if err != nil {
		return nil, fmt.Errorf("gc: build live set: %w", err)
	}
	live := make(map[cellKey]bool)
	for _, rec := range recs {
		for _, rr := range rec.Repos {
			live[cellKey{rec.Task, rr.Repo}] = true
		}
	}
	return live, nil
}

// observeCandidate reads the four reclamation signals for one on-disk worktree cell and
// returns the Candidate for Classify. It never moves a ref or dirties a worktree — it
// reads the marker ref, the worktree HEAD, and `git status`, and joins the precomputed
// live set. The worktree path is resolved through layout.Isolate so a maliciously-named
// task/repo directory is rejected loudly (ValidateSegment) rather than swept.
func observeCandidate(ctx context.Context, l layout.Layout, g *git.Git, task, repo string, live map[cellKey]bool) (Candidate, error) {
	ssot, err := l.Repo(repo)
	if err != nil {
		return Candidate{}, err
	}
	wt, err := l.Isolate(task, repo)
	if err != nil {
		return Candidate{}, err
	}

	markerSHA, hasMarker, err := g.OwnedRefSHA(ctx, ssot, task, repo)
	if err != nil {
		return Candidate{}, err
	}
	clean, err := g.IsClean(ctx, wt)
	if err != nil {
		return Candidate{}, err
	}

	cand := Candidate{
		Task:      task,
		Repo:      repo,
		HasMarker: hasMarker,
		Live:      live[cellKey{task, repo}],
		Clean:     clean,
	}
	// AheadOfBase only has meaning relative to the marker's recorded base (the v0
	// "the marker IS the base evidence" convention). For a markerless orphan the
	// marker gate short-circuits the verdict, so HEAD need not even be read.
	if hasMarker {
		head, err := g.ResolveRef(ctx, wt, "HEAD")
		if err != nil {
			return Candidate{}, err
		}
		cand.AheadOfBase = head != markerSHA
	}
	return cand, nil
}
