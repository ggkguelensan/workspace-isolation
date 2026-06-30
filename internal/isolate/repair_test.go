package isolate_test

import (
	"os"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// TestClassifyEvidencePositive pins the full 2×2 truth table of the drift
// classifier (HEAL-1, DESIGN §7.1/§7.4). The verdict is a pure function of the two
// PHYSICAL ownership signals — does wi's owned marker ref prove wi created the cell,
// and does the worktree exist on disk — because §7.1 makes the MARKER, not the
// registry record, the authority on whether a cell should exist.
//
// The two load-bearing rows are the no-resurrection keystone:
//   - (marker present, worktree absent) → MissingWorktree: safe to re-materialize
//     PRECISELY because the surviving marker proves this is not a completed-then-
//     deleted op.
//   - (marker absent, worktree absent) → Reclaimed: a completed-then-deleted op has
//     had its marker unlinked by isolate rm, so it must NEVER be resurrected.
func TestClassifyEvidencePositive(t *testing.T) {
	cases := []struct {
		name           string
		markerExists   bool
		worktreeExists bool
		want           isolate.Classification
	}{
		{"owned-and-present", true, true, isolate.ClassConsistent},
		{"owned-but-worktree-gone", true, false, isolate.ClassMissingWorktree},
		{"worktree-without-marker", false, true, isolate.ClassOrphanWorktree},
		{"neither-present", false, false, isolate.ClassReclaimed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isolate.Classify(tc.markerExists, tc.worktreeExists)
			if got != tc.want {
				t.Errorf("Classify(marker=%v, worktree=%v) = %q, want %q",
					tc.markerExists, tc.worktreeExists, got, tc.want)
			}
		})
	}
}

// TestClassifyNoResurrection isolates the single most dangerous direction: a cell
// with NO surviving marker and NO worktree must classify as Reclaimed, never as a
// re-materialize candidate. A classifier that keys re-materialization off the
// worktree's absence alone (ignoring the marker) would resurrect a deliberately
// removed isolate — the exact data-creation path DESIGN §7.4 HEAL-1 forbids. This is
// the registered non-vacuity guard: the mutant
//
//	case !worktreeExists: return ClassMissingWorktree   // BUG: ignores the marker
//
// reddens here (neither-present reads MissingWorktree, want Reclaimed) while every
// other row stays green, proving the test pins the marker as the re-materialize
// authority.
func TestClassifyNoResurrection(t *testing.T) {
	if got := isolate.Classify(false, false); got != isolate.ClassReclaimed {
		t.Fatalf("a cell with no marker and no worktree must be Reclaimed (no resurrection), got %q", got)
	}
	if got := isolate.Classify(true, false); got != isolate.ClassMissingWorktree {
		t.Fatalf("a cell whose marker survives but worktree is gone must be MissingWorktree, got %q", got)
	}
}

// TestInspectObservesEachCell drives the read-only drift observer (HEAL-1, DESIGN
// §7.4) over a real isolate. Inspect loads the registry record and, for each
// recorded repo, observes the two physical signals — the wi-owned marker ref (via
// git.OwnedRefSHA) and the worktree on disk (via the layout path) — and classifies
// the cell. It mutates nothing and dials no network.
//
// The test materializes a 4-repo isolate, then drives each repo into a distinct
// drift state by manipulating ONLY the disk/refs (never the registry record, which
// keeps saying "created" — so the record is deliberately stale for the drifted
// cells, which is exactly the divergence the reconciler must reconcile):
//
//	api   — left intact                               → Consistent
//	web   — worktree dir removed (marker survives)     → MissingWorktree
//	db    — marker ref deleted (worktree survives)     → OrphanWorktree
//	cache — BOTH worktree and marker removed           → Reclaimed
//
// MarkerSHA must be carried on the marker-bearing cells (api, web) — the actor
// re-materializes a MissingWorktree from the marker's recorded base sha — and empty
// on the marker-less cells (db, cache).
func TestInspectObservesEachCell(t *testing.T) {
	env, l, g, ctx := setup(t)
	names := []string{"api", "web", "db", "cache"}
	for _, n := range names {
		cloneSSOT(t, env, l, g, ctx, n)
	}
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_inspect", specs(names...)); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	// Drive web → MissingWorktree: remove the worktree dir, leave the marker.
	webWT, _ := l.Isolate(task, "web")
	if err := os.RemoveAll(webWT); err != nil {
		t.Fatalf("RemoveAll(web worktree): %v", err)
	}
	// Drive db → OrphanWorktree: delete the marker, leave the worktree.
	dbSSOT, _ := l.Repo("db")
	if err := g.DeleteOwnedRef(ctx, dbSSOT, task, "db"); err != nil {
		t.Fatalf("DeleteOwnedRef(db): %v", err)
	}
	// Drive cache → Reclaimed: remove BOTH the worktree dir and the marker.
	cacheWT, _ := l.Isolate(task, "cache")
	if err := os.RemoveAll(cacheWT); err != nil {
		t.Fatalf("RemoveAll(cache worktree): %v", err)
	}
	cacheSSOT, _ := l.Repo("cache")
	if err := g.DeleteOwnedRef(ctx, cacheSSOT, task, "cache"); err != nil {
		t.Fatalf("DeleteOwnedRef(cache): %v", err)
	}

	cells, err := isolate.Inspect(ctx, l, g, task)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(cells) != len(names) {
		t.Fatalf("Inspect returned %d cells, want %d", len(cells), len(names))
	}

	want := map[string]struct {
		class     isolate.Classification
		hasMarker bool
	}{
		"api":   {isolate.ClassConsistent, true},
		"web":   {isolate.ClassMissingWorktree, true},
		"db":    {isolate.ClassOrphanWorktree, false},
		"cache": {isolate.ClassReclaimed, false},
	}
	for i, c := range cells {
		// Cells are returned in record order, so they track names[].
		if c.Repo != names[i] {
			t.Errorf("cell[%d].Repo = %q, want %q", i, c.Repo, names[i])
		}
		w := want[c.Repo]
		if c.Class != w.class {
			t.Errorf("%s classified %q, want %q", c.Repo, c.Class, w.class)
		}
		// The record was never touched, so every cell's recorded stage is created —
		// which is precisely why the drifted cells (web/db/cache) are drift, not
		// agreement.
		if c.Stage != state.StageCreated {
			t.Errorf("%s recorded stage = %q, want %q", c.Repo, c.Stage, state.StageCreated)
		}
		if w.hasMarker && c.MarkerSHA == "" {
			t.Errorf("%s has a surviving marker; MarkerSHA must be carried for re-materialization, got empty", c.Repo)
		}
		if !w.hasMarker && c.MarkerSHA != "" {
			t.Errorf("%s has no marker; MarkerSHA must be empty, got %q", c.Repo, c.MarkerSHA)
		}
	}
}

// TestInspectNoRecordPropagates pins that Inspect over a task with no registry
// record surfaces state.ErrNoRecord (the CLI maps it to not_found) rather than
// inventing an empty drift report — "the isolate does not exist" is distinct from
// "the isolate is drift-free".
func TestInspectNoRecordPropagates(t *testing.T) {
	_, l, g, ctx := setup(t)
	_, err := isolate.Inspect(ctx, l, g, "ghost")
	if err == nil {
		t.Fatal("Inspect over a nonexistent isolate returned nil error, want state.ErrNoRecord")
	}
}

// TestPlanActionTruthTable pins the full (Classification × stage) → RepairAction
// decision policy of the reconciler (HEAL-1, DESIGN §7.1/§7.4). PlanAction is the
// pure "decide" half: given a Cell's evidence-positive class and its recorded stage,
// it chooses the per-cell repair action the executor will carry out under the lock.
//
// Stage only refines the Consistent case (a cell materialized on disk but still
// recorded pending is a crash-after-materialize-before-stage-flip → heal the stage
// forward); for every other class the physical evidence decides alone.
func TestPlanActionTruthTable(t *testing.T) {
	cases := []struct {
		class isolate.Classification
		stage state.Stage
		want  isolate.RepairAction
	}{
		{isolate.ClassConsistent, state.StageCreated, isolate.RepairNone},
		{isolate.ClassConsistent, state.StagePending, isolate.RepairHealStage},
		{isolate.ClassMissingWorktree, state.StageCreated, isolate.RepairRematerialize},
		{isolate.ClassMissingWorktree, state.StagePending, isolate.RepairRematerialize},
		{isolate.ClassOrphanWorktree, state.StageCreated, isolate.RepairBlockOrphan},
		{isolate.ClassOrphanWorktree, state.StagePending, isolate.RepairBlockOrphan},
		{isolate.ClassReclaimed, state.StageCreated, isolate.RepairDropRecord},
		{isolate.ClassReclaimed, state.StagePending, isolate.RepairDropRecord},
	}
	for _, tc := range cases {
		got := isolate.PlanAction(isolate.Cell{Class: tc.class, Stage: tc.stage})
		if got != tc.want {
			t.Errorf("PlanAction(class=%q, stage=%q) = %q, want %q", tc.class, tc.stage, got, tc.want)
		}
	}
}

// TestPlanActionNeverAutoRemovesOrphan isolates the two §7 safety invariants the
// planner must encode:
//
//   - An OrphanWorktree (a worktree wi cannot prove it owns) is a HARD BLOCK —
//     never reconciled into any record-dropping or removing action (§7.1: unexplained
//     orphans are never auto-pruned).
//   - A Reclaimed cell is dropped, NEVER re-materialized — no resurrection at the
//     planning layer either (§7.4 HEAL-1).
//
// Registered non-vacuity mutant: change the OrphanWorktree arm to return
// RepairDropRecord (auto-clean the orphan) → both OrphanWorktree rows above and this
// test redden, while every other row stays green — proving the orphan hard-block is
// load-bearing.
func TestPlanActionNeverAutoRemovesOrphan(t *testing.T) {
	for _, stage := range []state.Stage{state.StagePending, state.StageCreated} {
		if got := isolate.PlanAction(isolate.Cell{Class: isolate.ClassOrphanWorktree, Stage: stage}); got != isolate.RepairBlockOrphan {
			t.Errorf("an orphan worktree must be a hard block, got %q (stage %q)", got, stage)
		}
		if got := isolate.PlanAction(isolate.Cell{Class: isolate.ClassReclaimed, Stage: stage}); got == isolate.RepairRematerialize {
			t.Errorf("a reclaimed cell must never be re-materialized (no resurrection), got %q (stage %q)", got, stage)
		}
	}
}

// TestRepairReconcilesAllDriftStates drives the HEAL-1 executor isolate.Repair over a
// real isolate exhibiting ALL FIVE reconcile actions at once, proving it dispatches
// each cell on PlanAction and carries the action out under the isolate-state lock:
//
//	api   — left intact                         Consistent+created   → none          (untouched)
//	auth  — recorded stage forced to pending     Consistent+pending   → heal_stage    (stage → created)
//	web   — worktree dir removed (marker kept)    MissingWorktree      → rematerialize (re-added at marker sha)
//	db    — marker deleted (worktree kept)        OrphanWorktree       → block_orphan  (HARD BLOCK, left intact)
//	cache — both removed                          Reclaimed            → drop_record   (stale entry dropped)
//
// The two §7 safety guarantees are asserted physically: the orphan (db) is NEVER
// removed (worktree still on disk, still in the record — §7.1), and the reclaimed cell
// (cache) is dropped but NEVER recreated (no resurrection — §7.4). The re-materialized
// web worktree must come back detached at the EXACT marker sha (the owned commit), not
// an arbitrary newer base. One orphan present ⇒ overall RepairBlocked.
func TestRepairReconcilesAllDriftStates(t *testing.T) {
	env, l, g, ctx := setup(t)
	names := []string{"api", "auth", "web", "db", "cache"}
	for _, n := range names {
		cloneSSOT(t, env, l, g, ctx, n)
	}
	const task = "feat"
	if _, err := isolate.New(ctx, l, g, task, "op_new", specs(names...)); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}

	// Capture web's marker sha (the re-materialize source) before disturbing it.
	webSSOT, _ := l.Repo("web")
	webMarker, ok, err := g.OwnedRefSHA(ctx, webSSOT, task, "web")
	if err != nil || !ok {
		t.Fatalf("read web marker: sha=%q ok=%v err=%v", webMarker, ok, err)
	}

	// auth → Consistent+pending: force the recorded stage back to pending, leaving the
	// marker and worktree intact (a crash-after-materialize-before-stage-flip).
	rec, err := state.Load(l.StateDir(), task)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	for i := range rec.Repos {
		if rec.Repos[i].Repo == "auth" {
			rec.Repos[i].Stage = state.StagePending
		}
	}
	if err := state.Store(l.StateDir(), rec); err != nil {
		t.Fatalf("state.Store (force auth pending): %v", err)
	}

	// web → MissingWorktree: remove the worktree dir, keep the marker.
	webWT, _ := l.Isolate(task, "web")
	if err := os.RemoveAll(webWT); err != nil {
		t.Fatalf("RemoveAll(web): %v", err)
	}
	// db → OrphanWorktree: delete the marker, keep the worktree.
	dbSSOT, _ := l.Repo("db")
	if err := g.DeleteOwnedRef(ctx, dbSSOT, task, "db"); err != nil {
		t.Fatalf("DeleteOwnedRef(db): %v", err)
	}
	// cache → Reclaimed: remove both worktree and marker.
	cacheWT, _ := l.Isolate(task, "cache")
	if err := os.RemoveAll(cacheWT); err != nil {
		t.Fatalf("RemoveAll(cache): %v", err)
	}
	cacheSSOT, _ := l.Repo("cache")
	if err := g.DeleteOwnedRef(ctx, cacheSSOT, task, "cache"); err != nil {
		t.Fatalf("DeleteOwnedRef(cache): %v", err)
	}

	res, err := isolate.Repair(ctx, l, g, task, "op_repair")
	if err != nil {
		t.Fatalf("isolate.Repair: %v", err)
	}

	// One orphan ⇒ the whole run is blocked.
	if res.Status != isolate.RepairBlocked {
		t.Errorf("Repair status = %q, want %q (db is an orphan hard block)", res.Status, isolate.RepairBlocked)
	}

	wantAction := map[string]isolate.RepairAction{
		"api":   isolate.RepairNone,
		"auth":  isolate.RepairHealStage,
		"web":   isolate.RepairRematerialize,
		"db":    isolate.RepairBlockOrphan,
		"cache": isolate.RepairDropRecord,
	}
	if len(res.Repos) != len(names) {
		t.Fatalf("Repair returned %d outcomes, want %d", len(res.Repos), len(names))
	}
	for i, oc := range res.Repos {
		if oc.Repo != names[i] {
			t.Errorf("outcome[%d].Repo = %q, want %q (record order)", i, oc.Repo, names[i])
		}
		if oc.Action != wantAction[oc.Repo] {
			t.Errorf("%s action = %q, want %q", oc.Repo, oc.Action, wantAction[oc.Repo])
		}
		switch oc.Repo {
		case "db":
			if oc.Done {
				t.Errorf("db (orphan) must NOT be marked done")
			}
			if oc.Reason == "" {
				t.Errorf("db (orphan) must carry an orphan_unexplained reason, got empty")
			}
		default:
			if !oc.Done {
				t.Errorf("%s should be done (action %q), got Done=false err=%v", oc.Repo, oc.Action, oc.Err)
			}
		}
	}

	// web re-materialized: worktree back on disk, detached at the EXACT marker sha.
	if _, err := os.Stat(webWT); err != nil {
		t.Errorf("web worktree not re-materialized: %v", err)
	}
	if head, herr := g.ResolveRef(ctx, webWT, "HEAD"); herr != nil || head != webMarker {
		t.Errorf("re-materialized web HEAD = %q (err %v), want the owned marker sha %q", head, herr, webMarker)
	}
	// db orphan worktree LEFT INTACT — §7.1 never auto-prunes an orphan.
	dbWT, _ := l.Isolate(task, "db")
	if _, err := os.Stat(dbWT); err != nil {
		t.Errorf("db orphan worktree must be left intact, but it is gone: %v", err)
	}

	// Record after reconcile: cache dropped (no resurrection); the rest kept with
	// healed stages.
	rec2, err := state.Load(l.StateDir(), task)
	if err != nil {
		t.Fatalf("state.Load after Repair: %v", err)
	}
	stageOf := map[string]state.Stage{}
	for _, rr := range rec2.Repos {
		stageOf[rr.Repo] = rr.Stage
	}
	if _, present := stageOf["cache"]; present {
		t.Errorf("cache (reclaimed) must be dropped from the record, but it is still present")
	}
	for _, n := range []string{"api", "auth", "web", "db"} {
		if _, present := stageOf[n]; !present {
			t.Errorf("%s must remain in the record, but it is gone", n)
		}
	}
	if stageOf["auth"] != state.StageCreated {
		t.Errorf("auth stage = %q after heal, want %q", stageOf["auth"], state.StageCreated)
	}
}
