package isolate

// This file is the evidence-positive classification core of the three-way isolate
// drift reconciler (`isolate repair`, DESIGN §7.4 HEAL-1). The reconciler reconciles
// three sources for each repo cell of an isolate — the durable registry stage
// (intended), the wi-owned marker ref refs/wi/owned/<task>/<repo> (proven), and the
// worktree on disk (actual). This file owns the verdict over the two PHYSICAL
// ownership signals; joining that verdict with the recorded stage to pick a repair
// action (re-materialize / drop a stale record / heal a lagging stage) is the
// reconciler's job, built on top of this.

// Classification is the evidence-positive verdict for one repo cell of an isolate
// (DESIGN §7.1, §7.4 HEAL-1). It is a pure function of the two physical ownership
// signals: whether wi's owned marker ref proves wi created the cell, and whether the
// worktree exists on disk.
//
// The verdict keys on the MARKER, not the registry record, because §7.1 makes the
// marker the authority on whether a cell should exist. This is precisely what makes
// the HEAL-1 "no resurrection" rule fall out for free: isolate rm unlinks the marker
// when it reclaims a cell, so a completed-then-deleted op can never present a
// surviving marker — it can only ever classify as Reclaimed, never as a
// re-materialize candidate. Reclamation stays evidence-positive in both directions:
// wi recreates only what it can still prove it owns, and removes nothing it cannot.
type Classification string

const (
	// ClassConsistent: marker present AND worktree present — wi owns the cell and it
	// is materialized. The healthy steady state; the reconciler performs no disk
	// action (it may still heal a lagging registry stage forward to created).
	ClassConsistent Classification = "consistent"

	// ClassMissingWorktree: marker present, worktree absent — wi owns and still
	// intends the cell (the marker survives) but the worktree directory is gone (an
	// external rm, a crash mid-materialize, a pruned checkout). The re-materialize
	// candidate. It is safe to recreate EXACTLY because the surviving marker proves
	// this is not a completed-then-deleted op (no resurrection — DESIGN §7.4 HEAL-1).
	ClassMissingWorktree Classification = "missing_worktree"

	// ClassOrphanWorktree: worktree present, marker absent — a worktree wi cannot
	// prove it owns. A HARD BLOCK surfaced loudly as orphan_unexplained (DESIGN §7.1):
	// never auto-removed, never --force'd. wi will not reclaim what it cannot prove.
	ClassOrphanWorktree Classification = "orphan_worktree"

	// ClassReclaimed: neither marker nor worktree present — the cell was reclaimed
	// (completed-then-deleted) or never materialized past its durable intent. NO
	// resurrection: the absent marker is authoritative that the cell should not exist.
	// A registry record still naming it is a stale tombstone the reconciler may drop,
	// but the cell is never recreated.
	ClassReclaimed Classification = "reclaimed"
)

// Classify returns the evidence-positive verdict for a repo cell from its two
// physical ownership signals (marker ref present, worktree present). See
// Classification for why the verdict keys on the marker — that choice is what makes
// the no-resurrection guarantee structural rather than a special case.
func Classify(markerExists, worktreeExists bool) Classification {
	switch {
	case markerExists && worktreeExists:
		return ClassConsistent
	case markerExists && !worktreeExists:
		return ClassMissingWorktree
	case !markerExists && worktreeExists:
		return ClassOrphanWorktree
	default:
		return ClassReclaimed
	}
}
