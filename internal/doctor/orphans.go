package doctor

import (
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/gc"
)

// DetectOrphans is the orphan-inventory detector (DESIGN §7.5, the "orphan
// inventory — orphan_unexplained is loud" battery). It is the first of doctor's
// eight detectors and the model for the rest: a PURE function from injected
// observations (here gc.Candidate — what gc.Inspect already gathers from disk +
// refs + the registry) to []Finding, with all IO left to the caller. The doctor
// command (a later unit) runs gc.Inspect once and feeds the candidates here, so
// the diagnosis stays read-only and the detector stays trivially testable.
//
// It deliberately REUSES gc.Classify rather than re-deriving "what is an orphan":
// gc is the sole owner of the §7.1 evidence-positive verdict, and a second copy of
// that logic in doctor would be a drift hazard (and would risk the exact data-loss
// the verdict guards). Sharing the classifier is also the composition §7.5 intends
// — doctor diagnoses with the same eyes the safe-tier heal (gc) acts with, so a
// `--fix` that dispatches to gc can never disagree with what doctor reported.
//
// Scope: this detector surfaces ONLY ClassOrphanUnexplained — a worktree with NO
// wi-owned marker, which wi cannot prove it created. That is the loud cell §7.5
// names. A live cell (ClassLive short-circuits in Classify) is HEAL-1's to
// reconcile, never an orphan; a clean wi-owned leftover (ClassReclaimable) is gc's
// to collect, not a fault; and wi-owned work-carrying cells (ClassBlockedWork)
// belong to the separate three-way isolate-drift detector. Each orphan maps to a
// loud ERROR finding — Kind conflict (the same refusal gc gives a blocked sweep,
// exit 4), sub-code orphan_unexplained — so via WorstExit an orphan makes a doctor
// run refuse: an unexplained worktree is never silently tolerated.
//
// Guard DOCTOR-ORPHANS.
func DetectOrphans(cands []gc.Candidate) []Finding {
	var out []Finding
	for _, c := range cands {
		if gc.Classify(c) != gc.ClassOrphanUnexplained {
			continue
		}
		out = append(out, Finding{
			Detector: "orphans",
			Kind:     contract.KindConflict,
			Code:     "orphan_unexplained",
			Severity: SeverityError,
			Message: fmt.Sprintf(
				"unexplained orphan worktree %s/%s: no wi-owned marker proves wi created it — left untouched, resolve it deliberately",
				c.Task, c.Repo),
			Repo: c.Repo,
			Task: c.Task,
		})
	}
	return out
}
