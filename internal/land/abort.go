package land

import (
	"context"
	"errors"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// AbortStatus is the overall outcome of an Abort.
type AbortStatus string

const (
	// AbortStatusAborted: every landed repo was rewound to its pre-land base and the
	// parked record discarded — the land is fully undone (decision #ABORT-DISPOSE).
	AbortStatusAborted AbortStatus = "aborted"
	// AbortStatusBlocked: at least one landed repo could NOT be rewound because its base
	// moved past the landed tip since the land (a *git.StaleBaseRefError) — abort refused
	// that repo (its base untouched), rewound the rest, and KEPT the record (rewritten to
	// reflect which repos are now un-landed) so a later retry can finish.
	AbortStatusBlocked AbortStatus = "blocked"
)

// AbortRepoResult is one repo's outcome within an Abort, the value the CLI projects onto
// the envelope. Phase is the POST-abort phase: PhasePending for a repo successfully
// rewound (its base is back at the pre-land tip, the work still lives in the isolate), or
// PhaseLanded for a repo abort refused to rewind (still landed). RestoredTo is the backup
// sha the base was rewound to (set only on a successful rewind). Err carries a
// *git.StaleBaseRefError when abort refused the repo (a per-repo block, base untouched).
type AbortRepoResult struct {
	Repo       string
	Base       string
	Phase      landstate.Phase
	RestoredTo string
	Err        error
}

// AbortResult is the outcome of an Abort: the task identity, the overall Status, and a
// per-repo result in record order.
type AbortResult struct {
	Task   string
	OpID   string
	Status AbortStatus
	Repos  []AbortRepoResult
}

// Abort undoes a parked (or completed) land, rewinding every PhaseLanded repo's base back
// to the pre-land anchor (DESIGN §7.2). It is the inverse disposition of land.Run and the
// consumer of git.RestoreBaseRef's exact-match guard.
//
// Under the isolate-state:<task> lock (DESIGN §6.1) it loads the durable
// landstate.TaskLand record (no record → landstate.ErrNoRecord, which the CLI maps to
// not_found, mirroring `land status`), then for each landed repo calls
// git.RestoreBaseRef(base, expectCurrent=LandedSHA, restoreTo=BackupSHA) — the ONE
// sanctioned base-rewind path. The exact-match guard makes abort SAFE: if work was
// fast-forwarded onto the base past the landed tip since the land, the rewind is refused
// (*git.StaleBaseRefError) with the base left untouched, never clobbering that work.
//
// Disposition (decision #ABORT-DISPOSE): if EVERY landed repo rewound cleanly, the record
// is Delete'd (an aborted land is gone — `land status` → not_found). If any repo was
// stale-refused, abort rewinds the rest, rewrites the record reflecting the un-landed
// repos, and reports AbortStatusBlocked so a retry can finish once the base settles.
//
// specs supplies each repo's base branch (the record carries shas, not base names); the
// CLI resolves them from the manifest exactly as `land` does. A non-fast-forward refusal
// is NOT a Go error — it rides in the per-repo result; Abort's error return is reserved
// for failures that prevent the op at all (a held lock → *lock.HeldError → exit 6, an
// unresolvable ssot, or an infra fault from the ref move).
func Abort(ctx context.Context, l layout.Layout, g *git.Git, task, opID string, specs []RepoSpec) (AbortResult, error) {
	// Take the lock BEFORE reading the record: abort mutates base refs and the record, so
	// the load-rewind-dispose must be serialized against a concurrent land/abort (DESIGN
	// §6.1). `land status`, a pure read, deliberately takes no lock.
	key, err := lock.IsolateState(task)
	if err != nil {
		return AbortResult{}, err
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		return AbortResult{}, err // *lock.HeldError → exit 6 (DESIGN §6.1)
	}
	defer func() { _ = held.Release() }()
	_ = held.Stamp(opID)

	landDir := l.LandDir()
	rec, err := landstate.Load(landDir, task)
	if err != nil {
		return AbortResult{}, err // ErrNoRecord → CLI maps to not_found (mirrors `land status`)
	}

	baseOf := make(map[string]string, len(specs))
	for _, s := range specs {
		baseOf[s.Name] = s.Base
	}

	res := AbortResult{Task: task, OpID: opID, Status: AbortStatusAborted, Repos: make([]AbortRepoResult, 0, len(rec.Repos))}
	anyBlocked := false
	for i := range rec.Repos {
		cell := &rec.Repos[i]
		ar := AbortRepoResult{Repo: cell.Repo, Phase: cell.Phase}
		if cell.Phase != landstate.PhaseLanded {
			// Pending or blocked: this repo never advanced a base, so there is nothing to
			// rewind. It is dropped with the record on a full abort.
			res.Repos = append(res.Repos, ar)
			continue
		}

		base, ok := baseOf[cell.Repo]
		if !ok {
			return res, fmt.Errorf("land abort: no base known for landed repo %q (resolve it from the manifest)", cell.Repo)
		}
		ar.Base = base
		ssot, lerr := l.Repo(cell.Repo)
		if lerr != nil {
			return res, fmt.Errorf("land abort: locate ssot for %q: %w", cell.Repo, lerr)
		}

		if rerr := g.RestoreBaseRef(ctx, ssot, base, cell.LandedSHA, cell.BackupSHA); rerr != nil {
			var stale *git.StaleBaseRefError
			if errors.As(rerr, &stale) {
				// The base moved past the landed tip — refuse to rewind it, leaving the base
				// untouched (never clobber work landed on top). Keep the repo landed; the
				// record is retained so a later retry can finish.
				ar.Err = rerr
				anyBlocked = true
				res.Repos = append(res.Repos, ar)
				continue
			}
			return res, fmt.Errorf("land abort: restore base %q for %q: %w", base, cell.Repo, rerr)
		}

		// Rewound: the repo is no longer landed. The work still lives in the isolate; only
		// the base pointer moved back. Clear the anchors and mark the cell pending.
		ar.RestoredTo = cell.BackupSHA
		ar.Phase = landstate.PhasePending
		cell.Phase = landstate.PhasePending
		cell.BackupSHA = ""
		cell.LandedSHA = ""
		res.Repos = append(res.Repos, ar)
	}

	if anyBlocked {
		// Partial abort: rewrite the record reflecting the un-landed repos, keep the
		// stale-refused ones landed for a retry.
		res.Status = AbortStatusBlocked
		if serr := landstate.Store(landDir, rec); serr != nil {
			return res, fmt.Errorf("land abort: rewrite record for %q: %w", task, serr)
		}
		return res, nil
	}

	// Full abort: every landed repo rewound — discard the record (decision #ABORT-DISPOSE).
	if derr := landstate.Delete(landDir, task); derr != nil {
		return res, fmt.Errorf("land abort: discard record for %q: %w", task, derr)
	}
	return res, nil
}
