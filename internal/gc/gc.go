// Package gc is wi's evidence-positive garbage-collection core (HEAL-2, DESIGN §7.1).
// Where the isolate drift reconciler (HEAL-1) reconciles ONE task's recorded cells
// against their physical state, gc answers the workspace-wide question "which wi
// worktrees are safe to reclaim, and which must be left strictly alone?".
//
// The whole package exists to make §7.1 STRUCTURAL rather than a pile of special
// cases. Reclamation is evidence-POSITIVE: a worktree is collectable ONLY when a
// wi-owned marker ref refs/wi/owned/<task>/<repo> proves wi created it, AND it is
// clean, AND not ahead of base, AND not journaled as a live isolate. Anything else is
// preserved: an unexplained orphan (no marker proving wi's provenance) is a HARD
// BLOCK surfaced loudly as orphan_unexplained and is NEVER auto-pruned, and a
// wi-owned worktree that still carries uncommitted or unmerged work is refused so gc
// can never destroy live work (the HEAL-GC-NO-LIVE-LOSS guarantee). refs/wi/owned/*
// and refs/wi/backup/* are protected from collection in their own right.
//
// This file owns the pure verdict — a total function of the observed signals, no I/O.
// Gathering those signals from disk + refs + the registry (Inspect) and acting on the
// verdict (Collect) are separate units built on top of this core, exactly as
// isolate.Classify underpins isolate.Inspect/Repair.
package gc

// Candidate is the observed state of one wi worktree a gc sweep is deciding about,
// identified by its (task, repo) and carrying the four signals §7.1 keys reclamation
// on. Classify reads only the four booleans; Task/Repo are identity the caller
// threads through for bookkeeping and for the loud orphan_unexplained surface.
type Candidate struct {
	Task string
	Repo string

	// HasMarker reports whether the wi-owned marker ref refs/wi/owned/<task>/<repo>
	// exists — the evidence that PROVES wi created this worktree. Its absence is the
	// single fact that makes a candidate an unexplained orphan: no provenance, no
	// reclamation.
	HasMarker bool

	// Live reports whether a live isolate registry record still claims this cell
	// ("journaled as live"). A live candidate is never gc's to touch — reconciling a
	// live record against its physical state is HEAL-1's job, not gc's.
	Live bool

	// Clean reports whether the worktree has no uncommitted changes (a clean
	// `git status --porcelain`). Uncommitted work is work; gc must not delete it.
	Clean bool

	// AheadOfBase reports whether the worktree's branch has commits not reachable
	// from its base ref (unmerged committed work). Such work would be lost on
	// collection, so its presence refuses reclamation.
	AheadOfBase bool
}

// Class is gc's evidence-positive verdict for one Candidate (DESIGN §7.1). Like
// isolate.Classification it is an internal DOMAIN vocabulary, not a closed contract
// wire enum — the cli layer projects it into the frozen envelope's repos[]/blocked[]
// sub-codes; internal/contract remains the sole owner of the enums agents parse.
type Class string

const (
	// ClassLive: a live isolate record still claims this cell. gc NEVER collects a
	// live worktree — it short-circuits every other signal (encoding "not journaled
	// as live" as the first reclamation gate), so even a clean, wi-owned, behind-base
	// live cell is preserved. Defensive: a correct sweep need not surface live cells
	// as candidates at all, but classifying them is what makes "never collect live
	// work" structural rather than a property of the sweep's filtering.
	ClassLive Class = "live"

	// ClassReclaimable: NOT live, a marker proves wi created it, the worktree is clean
	// AND it is not ahead of base — every §7.1 condition met. The only class gc acts
	// on: a leftover from a completed-or-abandoned op that carries no work and is
	// provably wi's to remove.
	ClassReclaimable Class = "reclaimable"

	// ClassBlockedWork: NOT live and a marker proves wi created it, but the worktree
	// is dirty OR ahead of base — it carries uncommitted or unmerged work. A HARD
	// BLOCK: collecting it would destroy live work (HEAL-GC-NO-LIVE-LOSS). gc removes
	// nothing here and surfaces the cell so an operator can land or discard the work
	// deliberately.
	ClassBlockedWork Class = "blocked_work"

	// ClassOrphanUnexplained: NOT live and NO marker — wi cannot prove it created this
	// worktree, so it may be the user's own or the residue of lost state. A HARD BLOCK
	// surfaced loudly as orphan_unexplained (DESIGN §7.1): never auto-pruned, never
	// --force'd. wi reclaims only what it can prove it owns.
	ClassOrphanUnexplained Class = "orphan_unexplained"
)

// Classify returns the evidence-positive verdict for a gc candidate. It is total over
// the four signals and ordered so the two safety guarantees are structural:
//
//   - Live short-circuits everything: a journaled-live cell is never gc's to touch.
//   - The marker is the SECOND gate: without provenance a candidate can only ever be
//     ClassOrphanUnexplained — it can never reach ClassReclaimable regardless of how
//     clean it looks. This is the evidence-positive keystone (§7.1).
//   - Only once wi's provenance is proven do the work signals decide: any uncommitted
//     or unmerged work blocks reclamation (no live loss); a clean, behind-or-equal
//     worktree is the lone reclaimable case.
func Classify(c Candidate) Class {
	switch {
	case c.Live:
		return ClassLive
	case !c.HasMarker:
		return ClassOrphanUnexplained
	case !c.Clean || c.AheadOfBase:
		return ClassBlockedWork
	default:
		return ClassReclaimable
	}
}
