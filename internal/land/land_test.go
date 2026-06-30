package land_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/land"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// internal/land is the domain core of `wi land`: it returns an isolate's per-repo work
// into the SSOT base (DESIGN §1, §7.2) — the inverse of sync. This unit is the single
// repo-cell `LandRepo`, the irreducible step the per-task orchestrator composes:
//
//	1. resolve the isolate worktree's HEAD (the work tip the agent committed)
//	2. CreateBackupRef the base's CURRENT sha BEFORE any pointer move (the §7.2 anchor
//	   `land abort`/recovery restores from — never `git reset --hard`)
//	3. FastForwardBaseRef the base to the work tip — the SOLE base-mutation path
//	   (DESIGN §5: detached-HEAD + update-ref, ff-ONLY)
//
// A true fast-forward LANDS the work (PhaseLanded). A non-fast-forward (the base moved
// under us, or the work is behind) is a clean REFUSAL, not an error: the cell parks the
// repo at PhaseBlocked, leaving the base UNTOUCHED — it must NEVER force or rewind a
// base ref. The non-nil error return is reserved for infra faults (a resolve/anchor
// failure), so the orchestrator can tell "blocked, resume later" from "broken".
//
// Guard LAND-REPO-FF. Non-vacuity mutants (registered):
//   - skip the FastForwardBaseRef call (return Landed without moving) → TestLandRepoAdvancesBaseToWorkTip
//     RED (base ref not advanced to the work tip);
//   - on git.NonFastForwardError return PhaseLanded instead of PhaseBlocked → TestLandRepoRefusesNonFastForward
//     RED (a non-ff wrongly reported landed — the safety property);
//   - skip CreateBackupRef → the happy-path backup-anchor assertion RED.

// landScenario stands up a hermetic SSOT off a freshly seeded origin plus one isolate
// worktree detached at the base tip, and returns (ssot dir, worktree path, the base tip
// sha at worktree-creation time). The caller commits work in the worktree to advance its
// HEAD past baseTip.
func landScenario(t *testing.T, env *testenv.Env, g *git.Git, ctx context.Context) (ssot, wt, baseTip string) {
	t.Helper()
	origin := env.SeedOrigin(t, "acme")
	ssot = filepath.Join(env.Root, "ssot")
	if err := g.EnsureClone(ctx, ssot, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	baseTip = env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch)

	wt = filepath.Join(env.Root, "isolas", "taskx", "acme")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatalf("mkdir isolate dir: %v", err)
	}
	if err := g.AddWorktree(ctx, ssot, wt, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	return ssot, wt, baseTip
}

// commitWork writes a file in the worktree and commits it, returning the new HEAD sha —
// the work tip a land must carry into the base.
func commitWork(t *testing.T, env *testenv.Env, wt, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write work file: %v", err)
	}
	env.Git(t, wt, "add", file)
	env.Git(t, wt, "commit", "-m", "work")
	return env.Git(t, wt, "rev-parse", "HEAD")
}

func TestLandRepoAdvancesBaseToWorkTip(t *testing.T) {
	env := testenv.New(t)
	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	ssot, wt, baseTip := landScenario(t, env, g, ctx)
	workTip := commitWork(t, env, wt, "feature.txt", "the work\n")
	if workTip == baseTip {
		t.Fatalf("precondition: work tip must advance past base tip")
	}
	const task, repo = "taskx", "acme"

	oc, err := land.LandRepo(ctx, g, ssot, wt, task, repo, testenv.DefaultBranch)
	if err != nil {
		t.Fatalf("LandRepo: %v", err)
	}

	// Outcome reports a clean land carrying the work tip, anchored at the old base.
	if oc.Phase != landstate.PhaseLanded {
		t.Errorf("Phase = %q, want %q", oc.Phase, landstate.PhaseLanded)
	}
	if oc.LandedSHA != workTip {
		t.Errorf("LandedSHA = %q, want the work tip %q", oc.LandedSHA, workTip)
	}
	if oc.BackupSHA != baseTip {
		t.Errorf("BackupSHA = %q, want the pre-move base tip %q", oc.BackupSHA, baseTip)
	}

	// The SSOT base ref actually advanced to the work tip (verified with raw git).
	if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != workTip {
		t.Errorf("base ref after land = %q, want the work tip %q", got, workTip)
	}

	// The pre-move anchor was written under refs/wi/backup at the OLD base tip, so abort
	// has a restore point (§7.2).
	sha, exists, err := g.BackupRefSHA(ctx, ssot, task, repo)
	if err != nil {
		t.Fatalf("BackupRefSHA: %v", err)
	}
	if !exists || sha != baseTip {
		t.Errorf("backup anchor = (%q, %v), want (%q, true)", sha, exists, baseTip)
	}
}

func TestLandRepoRefusesNonFastForward(t *testing.T) {
	env := testenv.New(t)
	g := git.New(gitexec.NewWithEnv("git", env.GitEnv()))
	ctx := context.Background()

	ssot, wt, baseTip := landScenario(t, env, g, ctx)
	commitWork(t, env, wt, "feature.txt", "the work\n") // work tip descends from baseTip

	// A COMPETING land advanced the base to a DIVERGENT commit after the isolate was
	// created: a second worktree off the same base tip, a different commit, and the base
	// ref moved to it. Now the isolate's work tip is no longer a fast-forward of the base.
	other := filepath.Join(env.Root, "other")
	if err := g.AddWorktree(ctx, ssot, other, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree(other): %v", err)
	}
	divergentBase := commitWork(t, env, other, "other.txt", "competing work\n")
	env.Git(t, ssot, "update-ref", "refs/heads/"+testenv.DefaultBranch, divergentBase)
	const task, repo = "taskx", "acme"

	oc, err := land.LandRepo(ctx, g, ssot, wt, task, repo, testenv.DefaultBranch)
	if err != nil {
		t.Fatalf("LandRepo on a non-ff must be a clean refusal, got error: %v", err)
	}

	// A non-ff parks the repo blocked, lands nothing...
	if oc.Phase != landstate.PhaseBlocked {
		t.Errorf("Phase = %q, want %q (a non-fast-forward is a refusal, not a land)", oc.Phase, landstate.PhaseBlocked)
	}
	if oc.LandedSHA != "" {
		t.Errorf("LandedSHA = %q, want empty (nothing landed on a refusal)", oc.LandedSHA)
	}

	// ...and the base ref is LEFT UNTOUCHED at the divergent tip — land must never force
	// or rewind a base ref (DESIGN §5, §7.2).
	if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != divergentBase {
		t.Errorf("base ref after refused land = %q, want it unchanged at %q", got, divergentBase)
	}
	if baseTip == divergentBase {
		t.Fatalf("precondition: the competing land must have moved the base")
	}
}
