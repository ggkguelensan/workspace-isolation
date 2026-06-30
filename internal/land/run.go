package land

import (
	"context"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// RepoSpec is one repo to land: its wi-internal name (→ repos/<name> SSOT,
// isolas/<task>/<name> worktree) and the EFFECTIVE base branch its work returns onto.
// The CLI resolves these from the manifest before calling Run, so this package stays
// decoupled from manifest parsing — symmetric with isolate.RepoSpec.
type RepoSpec struct {
	Name string
	Base string
}

// Status is the overall outcome of a land Run.
type Status string

const (
	// StatusLanded: every repo's work fast-forwarded onto its base.
	StatusLanded Status = "landed"
	// StatusBlocked: a repo refused (a non-fast-forward parked it) or faulted, so the
	// run stopped there with the later repos left pending — a parked, resumable land.
	StatusBlocked Status = "blocked"
)

// RepoResult is one repo's result within a land Run, the value the CLI projects onto the
// envelope. Phase mirrors the durable landstate.RepoLand cell (PhaseLanded on a clean
// fast-forward; PhaseBlocked on a refusal or fault; PhasePending for a repo the run
// never reached). BackupSHA is the pre-move base anchor (set whenever LandRepo got far
// enough to write it); LandedSHA is the work tip the base advanced to on a land. Err
// carries an infra fault that parked the repo (a non-fast-forward refusal sets
// PhaseBlocked with Err nil — a refusal is not an error).
type RepoResult struct {
	Repo      string
	Base      string
	Phase     landstate.Phase
	BackupSHA string
	LandedSHA string
	Err       error
}

// Result is the outcome of a land Run: the task identity, the overall Status, and a
// per-repo result in request order.
type Result struct {
	Task   string
	OpID   string
	Status Status
	Repos  []RepoResult
}

// Run lands a task's isolate work onto its SSOT bases (DESIGN §1, §7.2). Under the
// isolate-state:<task> lock (DESIGN §6.1) it first writes an all-pending
// landstate.TaskLand record — the durable statement of intent that makes the op
// resumable — then lands each repo in request order via the LandRepo cell, folding each
// outcome into the durable record and persisting after EVERY repo so a crash leaves the
// record reflecting exactly the repos already landed (the §6.3 durable-partial-success
// posture, mirrored from isolate.New).
//
// It is SEQUENTIAL and STOP-AT-FIRST-BLOCK: the first repo that refuses (a
// non-fast-forward parks it PhaseBlocked) or faults halts the run with Status
// StatusBlocked; every repo after it is left PhasePending and untouched. The parked
// record is what `land continue`/`land abort` (HEAL-5) resume from. A blocked repo is
// NOT a Go error — it is a recorded refusal; Run's error return is reserved for failures
// that prevent the op running at all (a held lock → *lock.HeldError → exit 6, or an
// unwritable record).
//
// Scope: Run owns the lock, the durable landstate record, and the per-repo loop. The
// op-journal lifecycle (journal.KindLand — the crash-recovery wrapper) is a separate
// follow-up unit, the same split isolate uses between removeCore (no journaling) and
// Remove (journaling), so the recovery Finisher can re-run the core without
// re-journaling (DESIGN §7.4).
func Run(ctx context.Context, l layout.Layout, g *git.Git, task, opID string, specs []RepoSpec) (Result, error) {
	key, err := lock.IsolateState(task)
	if err != nil {
		return Result{}, err
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		return Result{}, err // *lock.HeldError → exit 6 (DESIGN §6.1)
	}
	defer func() { _ = held.Release() }()
	// Record who holds the lock for the self-heal layer (best-effort, same posture as
	// isolate.New: a body-less lock reads as "unknown holder", never auto-broken).
	_ = held.Stamp(opID)

	landDir := l.LandDir()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	// Durable statement of intent: every repo PhasePending BEFORE any pointer move.
	rec := landstate.NewTaskLand(task, opID, names)
	if err := landstate.Store(landDir, rec); err != nil {
		return Result{}, fmt.Errorf("land: write initial record for %q: %w", task, err)
	}

	res := Result{Task: task, OpID: opID, Status: StatusLanded, Repos: make([]RepoResult, 0, len(specs))}
	for i := range specs {
		s := specs[i]
		rr := RepoResult{Repo: s.Name, Base: s.Base, Phase: landstate.PhasePending}

		ssot, lerr := l.Repo(s.Name)
		var wt string
		if lerr == nil {
			wt, lerr = l.Isolate(task, s.Name)
		}
		var oc RepoLandOutcome
		if lerr == nil {
			oc, lerr = LandRepo(ctx, g, ssot, wt, task, s.Name, s.Base)
		}

		rr.BackupSHA = oc.BackupSHA
		if lerr != nil {
			// An infra fault (unresolvable ref, failed anchor/update) parks this repo
			// blocked so `land continue` can retry it — it is not a Go error from Run.
			rr.Phase = landstate.PhaseBlocked
			rr.Err = lerr
		} else {
			rr.Phase = oc.Phase
			rr.LandedSHA = oc.LandedSHA
		}

		// Fold this repo's cell into the durable record and persist before moving on, so
		// a crash leaves the record reflecting exactly what happened (§6.3).
		setCell(&rec, s.Name, rr.Phase, rr.BackupSHA)
		if serr := landstate.Store(landDir, rec); serr != nil {
			return res, fmt.Errorf("land: persist record for %q after %s: %w", task, s.Name, serr)
		}
		res.Repos = append(res.Repos, rr)

		if rr.Phase != landstate.PhaseLanded {
			// Refusal or fault: stop here. The later repos are NOT attempted — they stay
			// PhasePending in both the durable record and the result.
			res.Status = StatusBlocked
			for _, rest := range specs[i+1:] {
				res.Repos = append(res.Repos, RepoResult{Repo: rest.Name, Base: rest.Base, Phase: landstate.PhasePending})
			}
			return res, nil
		}
	}
	return res, nil
}

// RunJournaled is the op-journal lifecycle wrapper around Run (DESIGN §7.4, HEAL-4) — the
// land mirror of isolate.Remove around removeCore. It records an intent→committed
// journal entry (Kind journal.KindLand) BEFORE the run so a process that DIES mid-run is
// rolled forward by the offline startup recovery, then folds the lock+record+per-repo
// work in via Run, and finally:
//
//   - pre-run failure (a held lock, an unwritable initial record — Run returns an error
//     with a zero Result, so no base ref ever moved): DROP the journal. The op never
//     crossed its commit point; there is nothing to roll forward.
//
//   - fault PAST the commit point (Run returns an error after it began persisting, e.g. a
//     mid-loop record-persist failure): LEAVE the journal at `committed` so the offline
//     startup rolls it forward (re-running the idempotent land). This is the ONLY case a
//     land journal is left behind by the normal path.
//
//   - a CLEAN run — StatusLanded OR a deliberately-parked StatusBlocked: append `done`
//     and Discard the journal. THE RULING (land DIFFERS from isolate.Remove here): a
//     parked block is NOT a crash. Its full state lives in the durable
//     .wi/land/<task>.json record, HEAL-5 `land continue`/`land abort` resume from THAT
//     (not the journal), and offline roll-forward cannot unblock a non-fast-forward
//     anyway (that needs a rebase, HEAL-5/HEAL-6) — so leaving the journal would pin a
//     futile retry forever. isolate.Remove, by contrast, leaves a blocked teardown at
//     `committed` because an orphan can later resolve and a re-run reclaims it; a land
//     block cannot self-resolve on a blind re-run.
//
// Guard LAND-JOURNAL. The recovery Finisher for journal.KindLand (re-running the
// non-journaling Run core, the FinishRemove→removeCore shape) is HEAL-5 follow-up work;
// until it is wired, a land journal left at `committed` by a real crash is surfaced by
// recovery's default case (left for retry), never silently dropped.
func RunJournaled(ctx context.Context, l layout.Layout, g *git.Git, task, opID string, specs []RepoSpec) (Result, error) {
	jdir := l.JournalDir()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	base := journal.Entry{OpID: opID, Kind: journal.KindLand, Task: task, Repos: names}

	intent := base
	intent.Phase = journal.PhaseIntent
	if err := journal.Append(jdir, intent); err != nil {
		return Result{}, fmt.Errorf("land: journal intent for %q: %w", task, err)
	}
	committed := base
	committed.Phase = journal.PhaseCommitted
	if err := journal.Append(jdir, committed); err != nil {
		return Result{}, fmt.Errorf("land: journal committed for %q: %w", task, err)
	}

	res, err := Run(ctx, l, g, task, opID, specs)
	switch {
	case err != nil && res.Status == "":
		// Pre-run failure: no base ref moved. Drop the journal — nothing to roll forward.
		_ = journal.Discard(jdir, opID)
		return res, err
	case err != nil:
		// Fault past the commit point: leave the journal at `committed` for roll-forward.
		return res, err
	}

	// Clean run (StatusLanded or a parked StatusBlocked): close the lifecycle and
	// self-clean — see THE RULING above.
	done := base
	done.Phase = journal.PhaseDone
	if aerr := journal.Append(jdir, done); aerr != nil {
		return res, fmt.Errorf("land: journal done for %q: %w", task, aerr)
	}
	if derr := journal.Discard(jdir, opID); derr != nil {
		return res, fmt.Errorf("land: discard journal for %q: %w", task, derr)
	}
	return res, nil
}

// setCell folds a repo's phase + backup anchor into the durable record in place. The
// repo is always present (NewTaskLand created a cell for every spec).
func setCell(rec *landstate.TaskLand, repo string, phase landstate.Phase, backup string) {
	for i := range rec.Repos {
		if rec.Repos[i].Repo == repo {
			rec.Repos[i].Phase = phase
			rec.Repos[i].BackupSHA = backup
			return
		}
	}
}
