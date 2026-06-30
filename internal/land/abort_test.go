package land_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/land"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// land.Abort is the inverse disposition of land.Run (DESIGN §7.2, HEAL-5 abort leaf 2):
// under the isolate-state:<task> lock it loads the parked landstate record and rewinds
// every PhaseLanded repo's base back to its pre-land BackupSHA via git.RestoreBaseRef —
// the ONE sanctioned base-rewind path, whose exact-match guard (expectCurrent=LandedSHA)
// refuses to clobber work fast-forwarded onto the base since the land. Disposition
// (decision #ABORT-DISPOSE): all-rewound → Delete the record (an aborted land is gone,
// `land status` → not_found); a stale-refused repo → keep it landed, rewrite the record,
// report AbortStatusBlocked so a retry can finish.
//
// Guard LAND-ABORT. Non-vacuity mutants (registered):
//   - treat a git.StaleBaseRefError as a hard failure (drop the errors.As per-repo-block
//     arm, return the error) → TestAbortRefusesStaleRepoAndKeepsRecord RED (a recoverable
//     per-repo refusal mis-reported as a broken abort; the kept record + blocked status
//     assertions fail) while the clean-abort test stays GREEN (two-sided);
//   - skip the landstate.Delete on a full abort → TestAbortRewindsLandedReposAndDeletesRecord
//     RED on the record-deleted assertion only (the rewind assertions stay GREEN).

func TestAbortRewindsLandedReposAndDeletesRecord(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	names := []string{"api", "web"}
	for _, n := range names {
		landCloneSSOT(t, env, l, g, ctx, n)
	}
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_iso", isoSpecs(names...)); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	baseTip := map[string]string{}
	for _, n := range names {
		ssot, _ := l.Repo(n)
		baseTip[n] = env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
		wt, _ := l.Isolate(task, n)
		commitWork(t, env, wt, "feature.txt", "work in "+n+"\n")
	}
	if _, err := land.Run(ctx, l, g, task, "op_land", landSpecs(names...)); err != nil {
		t.Fatalf("land.Run: %v", err)
	}

	res, err := land.Abort(ctx, l, g, task, "op_abort", landSpecs(names...))
	if err != nil {
		t.Fatalf("land.Abort: %v", err)
	}
	if res.Status != land.AbortStatusAborted {
		t.Errorf("Status = %q, want %q", res.Status, land.AbortStatusAborted)
	}
	for _, n := range names {
		ssot, _ := l.Repo(n)
		// The base ref is rewound to its PRE-land tip (the backup anchor) — pure ref
		// motion, the landed commit un-referenced from the base.
		if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != baseTip[n] {
			t.Errorf("%s base after abort = %q, want pre-land tip %q", n, got, baseTip[n])
		}
		ar := findAbortRepo(t, res, n)
		if ar.Phase != landstate.PhasePending {
			t.Errorf("%s post-abort Phase = %q, want pending (un-landed)", n, ar.Phase)
		}
		if ar.RestoredTo != baseTip[n] {
			t.Errorf("%s RestoredTo = %q, want pre-land tip %q", n, ar.RestoredTo, baseTip[n])
		}
	}
	// The record is discarded — an aborted land is gone (decision #ABORT-DISPOSE).
	if _, err := landstate.Load(l.LandDir(), task); !errors.Is(err, landstate.ErrNoRecord) {
		t.Errorf("record after full abort: want ErrNoRecord, got %v", err)
	}
}

func TestAbortNoRecordIsErrNoRecord(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	landCloneSSOT(t, env, l, g, ctx, "api")
	if _, err := land.Abort(ctx, l, g, "never-landed", "op_abort", landSpecs("api")); !errors.Is(err, landstate.ErrNoRecord) {
		t.Errorf("Abort(no record): want ErrNoRecord, got %v", err)
	}
}

func TestAbortRefusesStaleRepoAndKeepsRecord(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	landCloneSSOT(t, env, l, g, ctx, "api")
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_iso", isoSpecs("api")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	ssot, _ := l.Repo("api")
	wt, _ := l.Isolate(task, "api")
	commitWork(t, env, wt, "feature.txt", "api work\n")
	if _, err := land.Run(ctx, l, g, task, "op_land", landSpecs("api")); err != nil {
		t.Fatalf("land.Run: %v", err)
	}
	landedTip := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch)

	// Simulate NEW work fast-forwarded onto the base past the landed tip (a later land, or
	// another agent). Abort must now refuse to rewind api — clobbering this would discard
	// work it never landed.
	other := filepath.Join(env.Root, "other-api")
	if err := g.AddWorktree(ctx, ssot, other, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree(other): %v", err)
	}
	newTip := commitWork(t, env, other, "later.txt", "later work\n")
	env.Git(t, ssot, "update-ref", "refs/heads/"+testenv.DefaultBranch, newTip)
	if newTip == landedTip {
		t.Fatalf("precondition: the base must have advanced past the landed tip")
	}

	res, err := land.Abort(ctx, l, g, task, "op_abort", landSpecs("api"))
	if err != nil {
		t.Fatalf("a stale repo is a per-repo refusal, not a Go error: %v", err)
	}
	if res.Status != land.AbortStatusBlocked {
		t.Errorf("Status = %q, want %q", res.Status, land.AbortStatusBlocked)
	}
	ar := findAbortRepo(t, res, "api")
	var stale *git.StaleBaseRefError
	if !errors.As(ar.Err, &stale) {
		t.Errorf("api abort Err = %v, want *git.StaleBaseRefError", ar.Err)
	}
	// The base is UNTOUCHED — abort never clobbered the work landed on top.
	if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != newTip {
		t.Errorf("api base after refused abort = %q, want it untouched at %q", got, newTip)
	}
	// The record is KEPT (rewritten) so a retry can finish — api still landed.
	rec, err := landstate.Load(l.LandDir(), task)
	if err != nil {
		t.Fatalf("record after blocked abort: want it kept, got %v", err)
	}
	if c := findCell(t, rec, "api"); c.Phase != landstate.PhaseLanded {
		t.Errorf("durable api Phase after refused abort = %q, want still landed", c.Phase)
	}
}

func findAbortRepo(t *testing.T, res land.AbortResult, repo string) land.AbortRepoResult {
	t.Helper()
	for _, r := range res.Repos {
		if r.Repo == repo {
			return r
		}
	}
	t.Fatalf("repo %q absent from abort result", repo)
	return land.AbortRepoResult{}
}
