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
// EnsureClone/Fetch — the two RunNetwork verbs.
package sync

import (
	"context"
	"time"

	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/mirror"
)

// RepoSpec is one repo to sync: its wi-internal name (→ repos/<name> SSOT), the
// origin URL to clone/fetch from, and the EFFECTIVE base branch wi keeps as the
// SSOT base. The CLI resolves these from the manifest (config.Repo) before calling
// Run, so this package stays decoupled from manifest parsing.
type RepoSpec struct {
	Name string
	URL  string
	Base string
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
		oc := syncOne(ctx, l, g, clk, specs[i])
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
func syncOne(ctx context.Context, l layout.Layout, g *git.Git, clk clock.Clock, spec RepoSpec) RepoOutcome {
	oc := RepoOutcome{Repo: spec.Name, Base: spec.Base}

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

	ssotDir, err := l.Repo(spec.Name)
	if err != nil {
		oc.Err = err
		return oc
	}

	// Lazy materialization: clone the SSOT detached at the base tip on first sync,
	// a no-op once the mirror exists (DESIGN §5).
	if err := g.EnsureClone(ctx, ssotDir, spec.URL, spec.Base); err != nil {
		oc.Err = err
		return oc
	}
	// Refresh remote-tracking refs, then advance the base ref to the fetched tip.
	if err := g.Fetch(ctx, ssotDir, "origin"); err != nil {
		oc.Err = err
		return oc
	}
	originSHA, err := g.ResolveRef(ctx, ssotDir, "refs/remotes/origin/"+spec.Base)
	if err != nil {
		oc.Err = err
		return oc
	}
	if err := g.FastForwardBaseRef(ctx, ssotDir, spec.Base, originSHA); err != nil {
		oc.Err = err // *git.NonFastForwardError on a rewound/force-pushed origin
		return oc
	}

	// The base ref now points at exactly the fetched origin tip — we hold
	// repo:<name>, so nothing moved either ref between the fetch and the
	// fast-forward — so the local base is 0 commits behind origin as of this fetch.
	snap := mirror.Snapshot{
		Repo:                  spec.Name,
		Base:                  spec.Base,
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
