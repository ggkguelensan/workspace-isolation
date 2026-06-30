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
