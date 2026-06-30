package doctor

import (
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/mirror"
)

// DetectMirrorStaleness is the mirror-staleness detector (DESIGN §7.5, the
// "mirror staleness (WARNING only — never refreshes, never exit 6 here)"
// battery). Like every doctor detector it is a PURE function from injected
// observations to []Finding: here the observations are the cached mirror
// Snapshots the doctor command has already Loaded from <root>/.wi/mirrors
// (skipping repos with no snapshot — ErrNoSnapshot is an ABSENT freshness block,
// NOT staleness). No I/O happens here, which is also how the no-hidden-network
// invariant (DESIGN §2 #3) is kept on this path: nothing dials, and a Snapshot
// is the most current OFFLINE-knowable freshness signal because wi never
// auto-fetches.
//
// It REUSES mirror.Snapshot.Freshness().Stale rather than re-deriving the
// "behind origin as of the last fetch" test — mirror is the sole owner of that
// verdict (BehindOriginAsOfFetch > 0), so a second copy in doctor would be a
// drift hazard. This is the same composition the orphan detector uses with
// gc.Classify: doctor diagnoses staleness with the same eyes the mirror layer
// projects it, so what doctor reports can never disagree with the
// mirror_freshness block other commands emit.
//
// The keystone of §7.5: a stale mirror is a WARNING, never an error. Via
// WorstExit a WARNING is exit-neutral, so `wi doctor` NEVER exits 6 on a stale
// mirror and NEVER refreshes it — only the land path (HEAL-6) refuses on a stale
// mirror, because landing onto a behind base is unsafe; merely reporting health
// is not. doctor surfaces the staleness so the operator can `wi sync`, but
// refusing here would make a read-only diagnosis fail on a benign, expected
// condition.
//
// Guard DOCTOR-MIRROR.
func DetectMirrorStaleness(snaps []mirror.Snapshot) []Finding {
	var out []Finding
	for _, s := range snaps {
		if !s.Freshness().Stale {
			continue
		}
		out = append(out, Finding{
			Detector: "mirror",
			Kind:     contract.KindMirrorStale,
			Code:     "mirror_stale",
			Severity: SeverityWarning, // §7.5: WARNING only — doctor never exits 6 on staleness
			Message: fmt.Sprintf(
				"mirror for %s is %d commit(s) behind origin as of its last fetch (%s) — run `wi sync` to refresh; doctor never dials",
				s.Repo, s.BehindOriginAsOfFetch, s.FetchedAt),
			Repo: s.Repo,
		})
	}
	return out
}
