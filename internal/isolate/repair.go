package isolate

import (
	"context"
	"os"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

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

// Cell is the observed three-way state of one repo within an isolate: its recorded
// registry stage (intended), the base sha its wi-owned marker ref records (proven —
// empty when no marker survives), and the evidence-positive Classification reconciling
// those against the worktree on disk (actual). MarkerSHA is the re-materialization
// source for a ClassMissingWorktree cell — the base tip captured when the cell was
// created — so the actor recreates the worktree at the exact commit wi owns, never an
// arbitrary newer base.
type Cell struct {
	Repo      string
	Stage     state.Stage
	Class     Classification
	MarkerSHA string
}

// Inspect is the read-only drift observer for one isolate (HEAL-1, DESIGN §7.4): it
// loads the registry record for task and, for each recorded repo in record order,
// observes the two physical ownership signals — the wi-owned marker ref (via
// git.OwnedRefSHA) and the worktree directory on disk — and Classifies the cell. It
// is the data-gathering half of `isolate repair`, exactly as lock.List is for
// `lock break`: it takes no lock, mutates nothing, and dials no network (OwnedRefSHA
// is a local ref read; worktree presence is an os.Stat). The action half (re-
// materialize / drop a stale tombstone / heal a lagging stage) is built on top.
//
// A task with no record yields state.ErrNoRecord (the CLI maps it to not_found) — the
// isolate does not exist, which is distinct from a drift-free isolate. A genuine git
// or layout fault (ref read blew up, an unsafe name) is returned as an error: it is an
// environment failure, not a per-cell drift, since every drift state is already
// expressible through the (marker, worktree) signals.
func Inspect(ctx context.Context, l layout.Layout, g *git.Git, task string) ([]Cell, error) {
	rec, err := state.Load(l.StateDir(), task)
	if err != nil {
		return nil, err // state.ErrNoRecord → not_found; else a read fault
	}
	cells := make([]Cell, 0, len(rec.Repos))
	for _, rr := range rec.Repos {
		cell, err := observeCell(ctx, l, g, task, rr.Repo, rr.Stage)
		if err != nil {
			return nil, err
		}
		cells = append(cells, cell)
	}
	return cells, nil
}

// observeCell reads the two physical signals for one repo cell and classifies it. It
// never moves a ref or dirties a worktree — it only reads the marker ref and stats
// the worktree path.
func observeCell(ctx context.Context, l layout.Layout, g *git.Git, task, repo string, stage state.Stage) (Cell, error) {
	ssot, err := l.Repo(repo)
	if err != nil {
		return Cell{}, err
	}
	markerSHA, markerExists, err := g.OwnedRefSHA(ctx, ssot, task, repo)
	if err != nil {
		return Cell{}, err
	}
	wtPath, err := l.Isolate(task, repo)
	if err != nil {
		return Cell{}, err
	}
	worktreeExists := pathExists(wtPath)

	cell := Cell{Repo: repo, Stage: stage, Class: Classify(markerExists, worktreeExists)}
	if markerExists {
		cell.MarkerSHA = markerSHA
	}
	return cell, nil
}

// pathExists reports whether path exists on disk (of any type). A missing path — the
// signal that a worktree was removed — is the only "false"; any other stat error
// (e.g. a permission fault) conservatively reads as "present" so the reconciler never
// treats an inaccessible worktree as a re-materialize target it would clobber.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// RepairAction is the per-cell verdict of the reconciler's "decide" half — the single
// thing the executor will do to reconcile one Cell's three-way drift under the
// isolate-state lock. It is an isolate-package domain vocabulary, NOT a closed contract
// wire enum: like Classification and state.Stage it never crosses the envelope boundary
// directly (the cli layer projects it into repos[]/blocked[] reasons), so internal/
// contract — sole owner of the WIRE enums — does not own it. Per IMPLEMENTATION_PLAN §7
// open decisions, recording this split keeps "contract owns closed enums" precise:
// contract owns the enums agents parse, packages own their internal policy vocabularies.
type RepairAction string

const (
	// RepairNone: a ClassConsistent cell whose recorded stage already agrees (created).
	// The healthy steady state — the executor touches neither disk nor record.
	RepairNone RepairAction = "none"

	// RepairHealStage: a ClassConsistent cell (marker + worktree both present) whose
	// recorded stage still lags at pending — a crash AFTER materialize but BEFORE the
	// stage flip. The physical truth is "created"; the executor heals the registry stage
	// forward to match. No disk action — the worktree is already correct.
	RepairHealStage RepairAction = "heal_stage"

	// RepairRematerialize: a ClassMissingWorktree cell (marker survives, worktree gone).
	// The executor recreates the worktree at the marker's recorded base sha (Cell.MarkerSHA)
	// — never an arbitrary newer base — and leaves the marker as-is (it already exists).
	// Safe PRECISELY because the surviving marker proves this is not a completed-then-
	// deleted op (no resurrection — DESIGN §7.4 HEAL-1).
	RepairRematerialize RepairAction = "rematerialize"

	// RepairDropRecord: a ClassReclaimed cell (neither marker nor worktree). The absent
	// marker is authoritative that the cell should not exist; a registry record still
	// naming it is a stale tombstone the executor drops. The cell is NEVER recreated —
	// no resurrection, whatever the recorded stage said.
	RepairDropRecord RepairAction = "drop_record"

	// RepairBlockOrphan: a ClassOrphanWorktree cell (worktree present, marker absent) —
	// a worktree wi cannot prove it owns. A HARD BLOCK (DESIGN §7.1 orphan_unexplained):
	// the executor removes NOTHING and surfaces it loudly. wi never auto-prunes what it
	// cannot prove it created.
	RepairBlockOrphan RepairAction = "block_orphan"
)

// PlanAction is the pure "decide" half of the three-way reconciler (HEAL-1): it maps a
// Cell's evidence-positive Classification plus its recorded stage to the single
// RepairAction the executor will carry out. It performs no I/O and is the seam where the
// two §7 safety invariants live structurally — a ClassReclaimed cell can only ever map to
// RepairDropRecord (never re-materialized: no resurrection), and a ClassOrphanWorktree
// cell can only ever map to RepairBlockOrphan (never auto-removed: unexplained orphans are
// a hard block). The recorded stage refines ONLY the consistent case, distinguishing the
// healthy steady state from a stage that lagged behind a completed materialize.
func PlanAction(c Cell) RepairAction {
	switch c.Class {
	case ClassConsistent:
		if c.Stage == state.StageCreated {
			return RepairNone
		}
		return RepairHealStage
	case ClassMissingWorktree:
		return RepairRematerialize
	case ClassOrphanWorktree:
		return RepairBlockOrphan
	default: // ClassReclaimed
		return RepairDropRecord
	}
}
