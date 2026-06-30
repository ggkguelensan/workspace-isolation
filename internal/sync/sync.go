// Package sync is the domain core of `wi sync`: it materializes and advances each
// requested repo's SSOT mirror to its origin base tip, then records what the fetch
// observed as a freshness Snapshot (DESIGN §5). It is wi's only v0 command that
// dials the network, and — together with v1 `land` — one of exactly two paths that
// advance a base ref, which it does SOLELY through git.FastForwardBaseRef under the
// repo:<name> lock (DESIGN §5, §6.1).
//
// Orchestration. For each repo, independently, under that repo's repo:<name> lock
// (the same key `land` takes — this is what linearizes the freshness race):
//
//  1. git.EnsureClone           (lazy — clone the SSOT detached at the base tip on
//     first sync; a no-op once the mirror exists)
//  2. git.Fetch                 (update refs/remotes/origin/<base> from the network)
//  3. git.FastForwardBaseRef    (advance refs/heads/<base> to the fetched origin tip
//     — the SOLE base-ref mutation; a genuine
//     non-fast-forward, i.e. origin rewound/force-pushed,
//     leaves the ref untouched and surfaces
//     *git.NonFastForwardError)
//  4. mirror.Store              (a freshness Snapshot; behind=0 because the base was
//     just advanced to exactly the fetched origin tip
//     under the lock)
//
// Unlike isolate.New (STOP-on-first-fail, because an isolate is ONE coherent
// multi-repo workspace whose completed set must remain a resumable prefix), sync is
// CONTINUE-on-fail: repos are independent SSOTs with no inter-dependency, so a
// failure on one (an unreachable origin, a non-fast-forward) is recorded in that
// repo's outcome and the remaining repos are still synced. A per-repo failure is
// therefore NOT a Go error from Run; Run's error return is reserved for a failure
// that prevents the whole op from running. The overall Status is StatusPartial when
// any repo failed.
//
// SSOT invariants (DESIGN §5): the base ref moves ONLY via FastForwardBaseRef
// (append-only, ff-only), the SSOT stays detached with a pristine working tree
// (EnsureClone detaches; Fetch never checks out), and the network is dialed only by
// EnsureClone/Fetch and — at first sync, to resolve the base candidate list against
// origin — FirstExistingRemoteHead, the three RunNetwork verbs.
package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/mirror"
)

// RepoSpec is one repo to sync: its wi-internal name (→ repos/<name> SSOT), the
// origin URL to clone/fetch from, and the ordered base candidate list (defaults.base
// / repo.base, e.g. ["dev","main"] = "prefer dev, else main"). sync is the ONE place
// the candidate list is resolved against ORIGIN rather than the mirror: at first sync
// the single-branch mirror does not exist yet, so syncOne probes origin under the
// repo lock to pick the effective base before cloning. The CLI passes the manifest's
// candidate list straight through (config.Repo.Base) without pre-resolving it.
type RepoSpec struct {
	Name string
	URL  string
	Base []string
}

// Status is the overall outcome of a Run.
type Status string

const (
	// StatusComplete: every requested repo was materialized and advanced.
	StatusComplete Status = "complete"
	// StatusPartial: at least one repo failed; the rest were still synced (exit 2).
	StatusPartial Status = "partial"
)

// RepoOutcome is one repo's result within a Run. On success Snapshot is the freshly
// stored freshness Snapshot (its LocalBaseSHA is the advanced base tip and behind=0)
// and Err is nil; on failure Snapshot is the zero value and Err names what went
// wrong (an unreachable origin, *git.NonFastForwardError, a held *lock.HeldError).
type RepoOutcome struct {
	Repo     string
	Base     string
	Snapshot mirror.Snapshot
	Err      error
}

// Result is the outcome of a Run: the op identity, the overall Status, and a
// per-repo outcome in request order. The CLI projects this onto the envelope's
// repos[] and the exit code (complete → 0, partial → 2).
type Result struct {
	OpID   string
	Status Status
	Repos  []RepoOutcome
}

// Run syncs each repo in specs to its origin base, in request order, each under its
// own repo:<name> lock. See the package doc for the continue-on-fail contract. l
// must be Bootstrap'd (the lock and mirrors dirs must exist). opID identifies the
// op. The returned error is reserved for an op-level failure; in v0 every failure is
// per-repo, so Run returns a nil error and reports failures via Status/Repos.
func Run(ctx context.Context, l layout.Layout, g *git.Git, clk clock.Clock, opID string, specs []RepoSpec) (Result, error) {
	res := Result{OpID: opID, Status: StatusComplete, Repos: make([]RepoOutcome, 0, len(specs))}
	for i := range specs {
		oc := syncOne(ctx, l, g, clk, opID, specs[i])
		if oc.Err != nil {
			res.Status = StatusPartial
		}
		res.Repos = append(res.Repos, oc)
	}
	return res, nil
}

// syncOne performs one repo's sync under its repo:<name> lock and folds every
// failure into the returned outcome's Err (never panics, never returns early past
// the lock release). The base ref is advanced ONLY via FastForwardBaseRef, so a
// rewound origin is refused, not force-moved (DESIGN §5).
func syncOne(ctx context.Context, l layout.Layout, g *git.Git, clk clock.Clock, opID string, spec RepoSpec) RepoOutcome {
	// Provisional base (top preference) so an early, pre-resolution failure still
	// reports a branch; overwritten with the resolved base once we hold the lock.
	oc := RepoOutcome{Repo: spec.Name, Base: firstBase(spec.Base)}

	key, err := lock.Repo(spec.Name)
	if err != nil {
		oc.Err = err
		return oc
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		oc.Err = err // *lock.HeldError — the CLI maps this to kind=lock_held
		return oc
	}
	defer func() { _ = held.Release() }()
	// Record who holds the repo:<name> lock so the self-heal layer can later read
	// the holder and judge staleness (DESIGN §6 / §7.3). Best-effort: the flock is
	// the exclusion guarantee, so a failed metadata write must not fail the sync — a
	// body-less lock reads as "unknown holder" and is conservatively never broken.
	_ = held.Stamp(opID)

	ssotDir, err := l.Repo(spec.Name)
	if err != nil {
		oc.Err = err
		return oc
	}

	// Resolve the ordered candidate list to the single effective base, under the
	// lock. This is the ONE place resolution consults ORIGIN: before the first clone
	// there is no mirror, so probe origin with ls-remote; once the single-branch
	// mirror exists, read the candidates back against it.
	base, err := resolveSyncBase(ctx, g, ssotDir, spec.URL, spec.Base)
	if err != nil {
		oc.Err = err
		return oc
	}
	oc.Base = base

	// Lazy materialization: clone the SSOT detached at the base tip on first sync,
	// a no-op once the mirror exists (DESIGN §5).
	if err := g.EnsureClone(ctx, ssotDir, spec.URL, base); err != nil {
		oc.Err = err
		return oc
	}
	// Refresh remote-tracking refs, then advance the base ref to the fetched tip.
	if err := g.Fetch(ctx, ssotDir, "origin"); err != nil {
		oc.Err = err
		return oc
	}
	originSHA, err := g.ResolveRef(ctx, ssotDir, "refs/remotes/origin/"+base)
	if err != nil {
		oc.Err = err
		return oc
	}
	if err := g.FastForwardBaseRef(ctx, ssotDir, base, originSHA); err != nil {
		oc.Err = err // *git.NonFastForwardError on a rewound/force-pushed origin
		return oc
	}

	// The base ref now points at exactly the fetched origin tip — we hold
	// repo:<name>, so nothing moved either ref between the fetch and the
	// fast-forward — so the local base is 0 commits behind origin as of this fetch.
	snap := mirror.Snapshot{
		Repo:                  spec.Name,
		Base:                  base,
		FetchedAt:             clk.Now().UTC().Format(time.RFC3339),
		LocalBaseSHA:          originSHA,
		OriginBaseSHA:         originSHA,
		BehindOriginAsOfFetch: 0,
	}
	if err := mirror.Store(l.MirrorsDir(), snap); err != nil {
		oc.Err = err
		return oc
	}
	oc.Snapshot = snap
	return oc
}

// resolveSyncBase picks the single effective base from the ordered candidate list.
// Once the single-branch SSOT mirror exists it is the source of truth (read locally
// with FirstExistingBase); before the first clone there is no mirror, so origin is
// probed with FirstExistingRemoteHead (sync's deliberate exception — every other
// seam resolves against the mirror via internal/baseref). None of the candidates
// existing on origin is a per-repo failure, surfaced as the outcome's Err rather than
// a cryptic `clone --branch` error.
func resolveSyncBase(ctx context.Context, g *git.Git, ssotDir, originURL string, candidates []string) (string, error) {
	if g.MirrorExists(ctx, ssotDir) {
		branch, _, found, err := g.FirstExistingBase(ctx, ssotDir, candidates)
		if err != nil {
			return "", err
		}
		if found {
			return branch, nil
		}
		// A materialized mirror that tracks none of the candidates is anomalous; fall
		// back to the top preference and let the fetch/ff surface any real mismatch.
		return firstBase(candidates), nil
	}
	branch, found, err := g.FirstExistingRemoteHead(ctx, originURL, candidates)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("sync: none of the base candidates %v exist on origin %s", candidates, originURL)
	}
	return branch, nil
}

// firstBase returns the top-preference candidate, or "" for an empty list (a
// contract violation config.Parse rejects).
func firstBase(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}
