package doctor

import (
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
)

// DetectParkedLands is the parked-land half of doctor's "pending journal/parked
// ops" battery (DESIGN §7.5), HEAL-8 detector #4 — the sibling of
// DetectPendingOps (DOCTOR-PENDING), which covers the journal half. Like every
// doctor detector it is a PURE function from injected observations to []Finding:
// here the observations are the durable parked-land records the doctor command
// already loaded from <root>/.wi/land (one landstate.Load per task file). No I/O
// happens in this function.
//
// It REUSES landstate.TaskLand.Parked() rather than re-deriving "is this land
// done" from raw repo phases — landstate is the sole owner of that verdict, the
// same way journal owns the pending-op verdict (DOCTOR-PENDING) and gc owns the
// orphan verdict (DOCTOR-ORPHANS). doctor diagnoses parked work with the same
// eyes `land continue`/`land abort` (HEAL-5) act with, so a future `--fix` can
// never disagree with what doctor reported.
//
// Mapping: any record whose Parked() is true — at least one repo has not reached
// PhaseLanded (a blocked repo, or a repo a crash left PhasePending) — is mapped to
// a KindPartial ERROR finding, sub-code land_parked: exit 2, the MILDEST non-ok in
// the #DOCTOR-EXIT-WORST rank, identical to the journal pending-op signal ("the
// workspace is sound, an op just needs draining"). A parked land is a real (if
// mild) ERROR, not a WARNING: an unfinished land means the workspace is not fully
// clean, so it must move the exit off 0. A record whose every repo is PhaseLanded
// has finished cleanly (kept only for the abort window, #CONTINUE-DISPOSE) and an
// empty record (no cells) is vacuously settled — neither is parked, so neither is
// flagged.
//
// Guard DOCTOR-PARKED-LANDS.
func DetectParkedLands(lands []landstate.TaskLand) []Finding {
	var out []Finding
	for _, rec := range lands {
		if !rec.Parked() {
			continue // every repo reached PhaseLanded (or no repos) — nothing pending
		}
		unlanded := 0
		for _, rl := range rec.Repos {
			if rl.Phase != landstate.PhaseLanded {
				unlanded++
			}
		}
		out = append(out, Finding{
			Detector: "land",
			Kind:     contract.KindPartial,
			Code:     "land_parked",
			Severity: SeverityError,
			Message: fmt.Sprintf(
				"parked land %q: %d of %d repo(s) not yet landed — run `wi land continue %s` to finish the blocked repos or `wi land abort %s` to rewind",
				rec.Task, unlanded, len(rec.Repos), rec.Task, rec.Task),
			Task: rec.Task,
		})
	}
	return out
}
