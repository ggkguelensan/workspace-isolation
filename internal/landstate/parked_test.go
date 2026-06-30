package landstate_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
)

// TestParked pins the pure parked-land verdict (DESIGN §7.2): a land is parked
// iff at least one repo has NOT reached PhaseLanded. The verdict keys on the
// ABSENCE of PhaseLanded — not the presence of PhaseBlocked — so a repo a crash
// left PhasePending (before it could block) still counts the land as parked, and
// a record whose every repo landed is a finished land (kept only for the abort
// window), not parked.
//
// Registered non-vacuity mutant (PHASE-PARKED): flipping the comparison
//
//	if rl.Phase != PhaseLanded { return true }
//
// to `if rl.Phase == PhaseLanded { return true }` inverts the verdict — Parked
// would report true iff SOME repo IS landed. That reddens the all-landed row
// (mutant returns true on the first landed repo; want false) and the all-blocked
// row (mutant finds no landed repo and returns false; want true), while the mixed
// rows that happen to contain a landed repo still pass — proving the test pins the
// "absence of PhaseLanded" semantics, not just "any non-empty record".
func TestParked(t *testing.T) {
	land := func(repos ...landstate.RepoLand) landstate.TaskLand {
		return landstate.TaskLand{Task: "feat", OpID: "op_x", Repos: repos}
	}
	r := func(name string, p landstate.Phase) landstate.RepoLand {
		return landstate.RepoLand{Repo: name, Phase: p}
	}

	cases := []struct {
		name string
		rec  landstate.TaskLand
		want bool
	}{
		// No cells ⟹ nothing to finish: vacuously not parked.
		{"empty", land(), false},
		// Every repo landed: a clean finish kept for the abort window — not parked.
		{"all-landed", land(r("api", landstate.PhaseLanded), r("web", landstate.PhaseLanded)), false},
		{"single-landed", land(r("api", landstate.PhaseLanded)), false},
		// A blocked repo parks the land for continue/abort.
		{"one-blocked", land(r("api", landstate.PhaseLanded), r("web", landstate.PhaseBlocked)), true},
		{"all-blocked", land(r("api", landstate.PhaseBlocked)), true},
		// A pending repo (crash before it could block) still counts as parked —
		// the verdict keys on the absence of PhaseLanded, not on PhaseBlocked.
		{"one-pending", land(r("api", landstate.PhaseLanded), r("web", landstate.PhasePending)), true},
		{"all-pending", land(r("api", landstate.PhasePending), r("web", landstate.PhasePending)), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rec.Parked(); got != tc.want {
				t.Errorf("Parked() = %v, want %v for %+v", got, tc.want, tc.rec.Repos)
			}
		})
	}
}
