package land_test

import (
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/land"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// land.RunJournaled is the op-journal lifecycle wrapper around land.Run (DESIGN §7.4,
// HEAL-4), the land mirror of isolate.Remove around removeCore: it journals
// intent→committed BEFORE the run (so a crash mid-run is rolled forward), then on a
// CLEAN run closes the lifecycle (done) and self-cleans the journal (Discard).
//
// RULING (recorded — land DIFFERS from isolate.Remove here): a deliberately PARKED land
// (StatusBlocked, a non-fast-forward refusal) is NOT a crash — its full state lives in
// the durable .wi/land/<task>.json record, and offline roll-forward cannot unblock a
// non-ff anyway (that needs a rebase, HEAL-5/HEAL-6). So land discards its journal on
// BOTH StatusLanded AND a clean StatusBlocked; HEAL-5 `land continue`/`land abort`
// resume from the landstate record, NOT the journal. The journal is left at `committed`
// ONLY on a genuine fault past the commit point (a record-persist failure / a real
// crash) — the only case the offline startup must roll forward. isolate.Remove, by
// contrast, LEAVES a blocked teardown at committed because an orphan can later resolve
// and a re-run reclaims it; a land block cannot self-resolve on re-run.
//
// Guard LAND-JOURNAL. Non-vacuity mutants (registered):
//   - skip the final journal.Discard → BOTH tests RED (a journal op survives a clean run,
//     proving the wrapper genuinely writes the lifecycle and Discard is what clears it);
//   - leave the journal at committed on StatusBlocked (an early `return res, nil` before
//     done/Discard, the isolate.Remove posture) → ONLY the parked-block test RED (it
//     pins the ruling that a parked land does NOT leave a roll-forward journal).

func TestRunJournaledClearsJournalOnCleanLand(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	landCloneSSOT(t, env, l, g, ctx, "api")
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_iso", isoSpecs("api")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	wt, _ := l.Isolate(task, "api")
	commitWork(t, env, wt, "feature.txt", "work\n")

	res, err := land.RunJournaled(ctx, l, g, task, "op_land_j1", landSpecs("api"))
	if err != nil {
		t.Fatalf("land.RunJournaled: %v", err)
	}
	if res.Status != land.StatusLanded {
		t.Errorf("Status = %q, want %q", res.Status, land.StatusLanded)
	}

	// The lifecycle closed and self-cleaned: no journal remains for the offline startup
	// to roll forward.
	ops, err := journal.Scan(l.JournalDir())
	if err != nil {
		t.Fatalf("journal.Scan: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("journal not cleared after a clean land: %d op(s) remain (%+v)", len(ops), ops)
	}
}

func TestRunJournaledClearsJournalOnParkedBlock(t *testing.T) {
	env, l, g, ctx := landSetup(t)
	landCloneSSOT(t, env, l, g, ctx, "api")
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_iso", isoSpecs("api")); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	// Make api a non-fast-forward (a competing land moved its base to a divergent commit)
	// so the land REFUSES and parks.
	apiWt, _ := l.Isolate(task, "api")
	commitWork(t, env, apiWt, "feature.txt", "api work\n")
	apiSSOT, _ := l.Repo("api")
	other := filepath.Join(env.Root, "other-api")
	if err := g.AddWorktree(ctx, apiSSOT, other, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree(other): %v", err)
	}
	divergent := commitWork(t, env, other, "other.txt", "competing\n")
	env.Git(t, apiSSOT, "update-ref", "refs/heads/"+testenv.DefaultBranch, divergent)

	res, err := land.RunJournaled(ctx, l, g, task, "op_land_j2", landSpecs("api"))
	if err != nil {
		t.Fatalf("a parked block is not an error: %v", err)
	}
	if res.Status != land.StatusBlocked {
		t.Errorf("Status = %q, want %q", res.Status, land.StatusBlocked)
	}

	// THE RULING: a deliberately-parked block self-cleans the journal — it is NOT left at
	// `committed` for offline roll-forward. The parked state lives in the landstate
	// record (HEAL-5 resumes from there), and roll-forward cannot unblock a non-ff.
	ops, err := journal.Scan(l.JournalDir())
	if err != nil {
		t.Fatalf("journal.Scan: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("a parked block must not leave a roll-forward journal: %d op(s) remain (%+v)", len(ops), ops)
	}

	// Sanity: the durable parked record IS present (the block is recorded where HEAL-5
	// looks), so clearing the journal did not lose the parked state.
	rec, err := landstate.Load(l.LandDir(), task)
	if err != nil {
		t.Fatalf("landstate.Load: %v", err)
	}
	if c := findCell(t, rec, "api"); c.Phase != landstate.PhaseBlocked {
		t.Errorf("durable api Phase = %q, want blocked (parked state must survive journal cleanup)", c.Phase)
	}
}
