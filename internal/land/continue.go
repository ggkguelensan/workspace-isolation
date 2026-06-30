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

// Continue resumes a parked land (DESIGN §7.2, HEAL-5 leaf 4) — the forward dual of
// land.Abort over the SAME durable landstate.TaskLand record. Under the
// isolate-state:<task> lock (DESIGN §6.1) it loads the record and RE-ATTEMPTS LandRepo for
// every NON-landed cell (pending or blocked), landing those whose isolate work now
// fast-forwards its base and re-parking those that still refuse, while carrying every
// already-PhaseLanded cell through untouched (never re-moving a base already at its work
// tip). It folds each outcome into the record and persists after EVERY repo, so a crash
// mid-continue leaves the record reflecting exactly the repos landed so far (§6.3) — the
// same durable-partial posture as Run.
//
// It DIVERGES from Run's stop-at-first-block (decision #CONTINUE-ATTEMPT-ALL): wi repos
// land onto INDEPENDENT bases with no modeled cross-repo dependency, so a repo that still
// refuses must NOT hold back another that can now land. Continue attempts every non-landed
// cell and maximizes recovery progress — the forward dual of abort processing every landed
// repo. A repo that still refuses (a non-fast-forward) is a recorded refusal, parked
// PhaseBlocked with Err nil, NOT a Go error; an infra fault parks it PhaseBlocked WITH Err
// so a later continue can retry. Continue's error return is reserved for failures that
// prevent the op at all (no record → landstate.ErrNoRecord, which the CLI maps to
// not_found mirroring `land status`/`land abort`; a held lock → *lock.HeldError → exit 6;
// an unwritable record).
//
// Disposition (decision #CONTINUE-DISPOSE): continue KEEPS the record in BOTH outcomes (it
// never Delete's it). A fully-completed continue reaches the IDENTICAL durable state as a
// clean land.Run — an all-landed record that `land abort` can still rewind — and a residual
// block keeps it parked for a further continue/abort. This is the deliberate ASYMMETRY with
// abort, whose full-success terminal state is no record at all (#ABORT-DISPOSE): abort's
// job is to make the land gone, continue's is to make it complete, which is the same
// abortable state a fresh land leaves behind.
//
// specs supplies each non-landed repo's base branch (the record carries shas, not base
// names); the CLI resolves them from the manifest exactly as `land`/`land abort` do.
//
// Scope: like Run, Continue owns the lock, the durable record, and the per-repo loop; the
// op-journal lifecycle (journal.KindLand) is the separate RunJournaled-style wrapper.
//
// Guard LAND-CONTINUE.
func Continue(ctx context.Context, l layout.Layout, g *git.Git, task, opID string, specs []RepoSpec) (Result, error) {
	// Take the lock BEFORE reading the record: continue mutates base refs and the record,
	// so the load-attempt-persist must be serialized against a concurrent land/abort
	// (DESIGN §6.1) — the same posture as Run and Abort. `land status`, a pure read, takes
	// no lock.
	key, err := lock.IsolateState(task)
	if err != nil {
		return Result{}, err
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		return Result{}, err // *lock.HeldError → exit 6 (DESIGN §6.1)
	}
	defer func() { _ = held.Release() }()
	_ = held.Stamp(opID)

	landDir := l.LandDir()
	rec, err := landstate.Load(landDir, task)
	if err != nil {
		return Result{}, err // ErrNoRecord → CLI maps to not_found (mirrors `land status`/abort)
	}

	baseOf := make(map[string]string, len(specs))
	for _, s := range specs {
		baseOf[s.Name] = s.Base
	}

	res := Result{Task: task, OpID: opID, Status: StatusLanded, Repos: make([]RepoResult, 0, len(rec.Repos))}
	for i := range rec.Repos {
		cell := &rec.Repos[i]
		rr := RepoResult{Repo: cell.Repo, Base: baseOf[cell.Repo], Phase: cell.Phase}

		if cell.Phase == landstate.PhaseLanded {
			// Already landed by a prior land/continue — carry it through untouched. Its base
			// is already at the work tip; re-moving it would be a no-op at best and must not
			// re-anchor a new backup over the one `land abort` rewinds from.
			rr.BackupSHA = cell.BackupSHA
			rr.LandedSHA = cell.LandedSHA
			res.Repos = append(res.Repos, rr)
			continue
		}

		// A pending or blocked cell: RE-ATTEMPT it. #CONTINUE-ATTEMPT-ALL — unlike Run we do
		// NOT stop at the first repo that still refuses; every non-landed repo gets a fresh
		// attempt so an unblocked one lands even when an earlier one stays blocked.
		base, ok := baseOf[cell.Repo]
		if !ok {
			return res, fmt.Errorf("land continue: no base known for repo %q (resolve it from the manifest)", cell.Repo)
		}
		ssot, lerr := l.Repo(cell.Repo)
		var wt string
		if lerr == nil {
			wt, lerr = l.Isolate(task, cell.Repo)
		}
		var oc RepoLandOutcome
		if lerr == nil {
			oc, lerr = LandRepo(ctx, g, ssot, wt, task, cell.Repo, base)
		}

		rr.BackupSHA = oc.BackupSHA
		if lerr != nil {
			// An infra fault parks this repo blocked so a later continue can retry it — it is
			// not a Go error from Continue (mirrors Run).
			rr.Phase = landstate.PhaseBlocked
			rr.Err = lerr
		} else {
			rr.Phase = oc.Phase
			rr.LandedSHA = oc.LandedSHA
		}
		if rr.Phase != landstate.PhaseLanded {
			// A residual refusal/fault: the overall continue is still blocked, but we keep
			// going to attempt the remaining repos (#CONTINUE-ATTEMPT-ALL).
			res.Status = StatusBlocked
		}

		// Fold the cell into the durable record and persist before moving on, so a crash
		// mid-continue leaves the record reflecting exactly the repos landed so far (§6.3).
		setCell(&rec, cell.Repo, rr.Phase, rr.BackupSHA, rr.LandedSHA)
		if serr := landstate.Store(landDir, rec); serr != nil {
			return res, fmt.Errorf("land continue: persist record for %q after %s: %w", task, cell.Repo, serr)
		}
		res.Repos = append(res.Repos, rr)
	}

	// #CONTINUE-DISPOSE: KEEP the record in BOTH outcomes — never Delete it. A fully-landed
	// continue is the same abortable state a clean land.Run leaves; a residual block stays
	// parked for a further continue/abort. The per-repo Store above already wrote every
	// change, so the durable record is current; no final write is needed.
	return res, nil
}

// FinishLand is the recovery Finisher core for a rolled-forward land op (HEAL-5, DESIGN
// §7.4) — the land mirror of isolate.FinishRemove. The offline executor (via recovery, which
// resolves each repo's base from the manifest because the durable record stores shas, not
// base names) injects it for a land journal left at `committed`: a `wi land` that DIED
// mid-run, leaving a landstate record reflecting exactly the repos landed before the crash.
//
// It re-runs the NON-journaling Continue core — NOT Run: Continue LOADS the record and carries
// every already-PhaseLanded cell through untouched (preserving the backup anchor `land abort`
// rewinds from), re-attempting only the pending/blocked cells; a fresh Run would overwrite the
// record all-pending and re-anchor a new backup over an already-advanced base, corrupting the
// abort restore point. It journals NOTHING — the executor owns every journal mutation during
// recovery, so re-journaling here would double-write (DESIGN §7.4).
//
// The return value answers the executor's only question — "did the durable effect complete?":
//
//   - StatusLanded OR a residual StatusBlocked → nil. THE RULING (land DIVERGES from
//     isolate.FinishRemove, which errors on a still-blocked orphan): a land block is a
//     non-fast-forward that a BLIND re-run cannot resolve (it needs a rebase, HEAL-6), so
//     returning an error would pin a futile retry forever. The block's full state is durable
//     in the landstate record; `land continue`/`land abort` resume from THAT, not the journal
//     — so a recovered land is "done" the moment Continue completes, whether or not every repo
//     landed. (FinishRemove errors on a still-blocked orphan because an orphan CAN later
//     resolve and a re-run reclaims it — a land block cannot self-resolve; the same asymmetry
//     RunJournaled records when it self-cleans the journal on a parked block.)
//   - landstate.ErrNoRecord → nil: the land was torn down (a completed `land abort`) or
//     otherwise gone before the crash — idempotent no-op success, nothing left to roll forward
//     (mirrors FinishRemove's ErrNoRecord → nil).
//   - any other fault (a held lock, an unwritable record, an unknown base) → error: surfaced
//     so the executor LEAVES the journal for the next offline startup to retry.
//
// Guard HEAL-FINISH-LAND.
func FinishLand(ctx context.Context, l layout.Layout, g *git.Git, task, opID string, specs []RepoSpec) error {
	_, err := Continue(ctx, l, g, task, opID, specs)
	if errors.Is(err, landstate.ErrNoRecord) {
		return nil // land completed / torn down before the crash — idempotent no-op
	}
	if err != nil {
		return fmt.Errorf("land: finish land %q: %w", task, err)
	}
	// A clean Continue — StatusLanded OR a residual StatusBlocked — both roll forward: see THE
	// RULING above. We deliberately ignore the returned Status; a non-ff block is "done" for
	// roll-forward, not a retriable fault.
	return nil
}
