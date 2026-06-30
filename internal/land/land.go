// Package land returns an isolate's committed work into the SSOT base — the inverse
// of sync (DESIGN §1, §7.2). It is the domain core of `wi land`: pure git-ref motion
// over the SSOT clone, taking no LLM and dialing no network (the base advance is a
// local update-ref; DESIGN §2, §5).
//
// LandRepo is the irreducible repo-cell the per-task orchestrator composes once per
// repo. The orchestrator owns the durable landstate.TaskLand record and the
// isolate-state:<task> lock (DESIGN §6.1); this cell owns only the three git steps that
// must happen together in the right order — anchor, then advance — so the §7.2 safety
// properties (a backup ref exists before any pointer move; a base is only ever
// fast-forwarded, never forced or rewound) hold at the smallest possible scope.
package land

import (
	"context"
	"errors"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
)

// RepoLandOutcome is one repo's result within a land run, the value the orchestrator
// folds into the durable landstate.RepoLand cell. A clean fast-forward yields
// PhaseLanded with LandedSHA set to the work tip the base now points at; a refused
// non-fast-forward yields PhaseBlocked with LandedSHA empty and the base left untouched.
// BackupSHA is the pre-move base tip anchored under refs/wi/backup in BOTH cases (the
// anchor is written before the ff is attempted), giving `land abort` a restore point.
type RepoLandOutcome struct {
	Repo      string
	Base      string
	Phase     landstate.Phase
	BackupSHA string
	LandedSHA string
}

// LandRepo lands one repo's isolate work tip into its SSOT base.
//
// It resolves the worktree's HEAD (the agent's work tip), anchors the base's CURRENT
// tip under refs/wi/backup BEFORE touching any pointer (DESIGN §7.2 — the sha
// `land abort` restores, never `git reset --hard`), then fast-forwards the base ref to
// the work tip via git.FastForwardBaseRef, the SOLE base-mutation path (DESIGN §5).
//
// A non-fast-forward — the base moved under the isolate, or the work is behind — is a
// clean REFUSAL, not a failure: the outcome is parked at PhaseBlocked with the base
// left exactly as it was, and the returned error is nil so the orchestrator records
// "blocked, resume later" rather than aborting the whole run. The returned error is
// reserved for genuine infra faults (an unresolvable ref, a failed anchor or update),
// which the orchestrator surfaces as a broken land.
func LandRepo(ctx context.Context, g *git.Git, ssotDir, worktreePath, task, repo, base string) (RepoLandOutcome, error) {
	oc := RepoLandOutcome{Repo: repo, Base: base}

	workTip, err := g.ResolveRef(ctx, worktreePath, "HEAD")
	if err != nil {
		return oc, fmt.Errorf("land: resolve work tip for %s: %w", repo, err)
	}
	baseTip, err := g.ResolveRef(ctx, ssotDir, "refs/heads/"+base)
	if err != nil {
		return oc, fmt.Errorf("land: resolve base %q for %s: %w", base, repo, err)
	}

	// Anchor the pre-move base tip before any pointer move (DESIGN §7.2), so a crash or
	// refusal after this point still leaves a restore point for `land abort`.
	if err := g.CreateBackupRef(ctx, ssotDir, task, repo, baseTip); err != nil {
		return oc, fmt.Errorf("land: anchor backup for %s: %w", repo, err)
	}
	oc.BackupSHA = baseTip

	if err := g.FastForwardBaseRef(ctx, ssotDir, base, workTip); err != nil {
		var nff *git.NonFastForwardError
		if errors.As(err, &nff) {
			// Refusal, not failure: the base is untouched and the repo parks blocked.
			oc.Phase = landstate.PhaseBlocked
			return oc, nil
		}
		return oc, fmt.Errorf("land: advance base %q for %s: %w", base, repo, err)
	}

	oc.Phase = landstate.PhaseLanded
	oc.LandedSHA = workTip
	return oc, nil
}
