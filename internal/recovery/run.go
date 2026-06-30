package recovery

import (
	"context"
	"errors"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// Report summarizes one offline startup recovery pass for the caller (the startup hook)
// to surface. Skipped is true when the workspace-wide lock was already held by another
// process — a recovery pass is already in flight there, so this one DEFERRED to it and
// touched nothing (Journal is then the zero value). Otherwise Journal carries what the
// pass did, op by op.
type Report struct {
	Skipped bool
	Journal journal.RecoveryReport
}

// Run performs one OFFLINE roll-forward recovery pass over the workspace's journal
// (HEAL-4 sub-unit 3d-iv, DESIGN §7.4): it finishes every operation that crashed after
// its commit point, drops the journals of those that never committed (their partial
// artifacts are left to the evidence-positive heals), and reaps the stale journals of
// those that already completed. It dials NO network — both the journal-file I/O and the
// injected per-kind Finisher (recovery.Finisher → isolate.FinishRemove → the offline
// removeCore) are local — so it is safe to run unconditionally at startup, before any
// command body.
//
// It runs under lock.Workspace(), the coarsest key, taken NON-BLOCKING. If another
// process already holds it (another startup pass, mid-recovery), Run returns
// Report{Skipped: true} and does nothing: startup must never BLOCK on, nor DOUBLE-RUN, a
// recovery another process is already performing. The workspace lock serializes recovery
// PASSES against each other; it is NOT what keeps a live command from being stomped —
// that is the per-task isolate-state lock the Finisher's removeCore re-takes, which makes
// a roll-forward racing a live `isolate rm` fail closed (HeldError → that op left for the
// next startup to retry) instead of tearing the same isolate down twice.
//
// A non-HeldError acquire failure, or a fatal scan/filesystem fault from journal.Recover,
// is returned as an error. A single op's Finisher error is NOT fatal: journal.Recover
// records it in Report.Journal.Failed and leaves that op's journal for the next startup.
//
// Run requires an initialized workspace (l.LocksDir() must exist); the caller gates on
// that. It never creates workspace directories.
func Run(ctx context.Context, l layout.Layout, g *git.Git) (Report, error) {
	held, err := lock.Acquire(l.LocksDir(), lock.Workspace())
	if err != nil {
		var he *lock.HeldError
		if errors.As(err, &he) {
			return Report{Skipped: true}, nil
		}
		return Report{}, err
	}
	defer func() { _ = held.Release() }()

	rep, err := journal.Recover(l.JournalDir(), Finisher(ctx, l, g))
	if err != nil {
		return Report{}, err
	}
	return Report{Journal: rep}, nil
}
