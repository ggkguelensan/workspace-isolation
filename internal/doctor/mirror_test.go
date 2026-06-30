package doctor_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/doctor"
	"github.com/ggkguelensan/workspace-isolation/internal/mirror"
)

// TestDetectMirrorStaleness pins the mirror-staleness detector (DESIGN §7.5): it
// flags EXACTLY the repos whose cached Snapshot reports the local base behind
// origin (mirror.Snapshot.Freshness().Stale), and nothing else. A fresh mirror
// (behind == 0) is normal and produces no finding.
func TestDetectMirrorStaleness(t *testing.T) {
	stale := mirror.Snapshot{Repo: "api", FetchedAt: "2026-06-30T00:00:00Z", BehindOriginAsOfFetch: 3}
	fresh := mirror.Snapshot{Repo: "web", FetchedAt: "2026-06-30T00:00:00Z", BehindOriginAsOfFetch: 0}

	got := doctor.DetectMirrorStaleness([]mirror.Snapshot{stale, fresh})

	if len(got) != 1 {
		t.Fatalf("exactly the one stale mirror must be flagged, got %d findings: %+v", len(got), got)
	}
	f := got[0]
	if f.Kind != contract.KindMirrorStale {
		t.Errorf("stale finding Kind = %q, want %q", f.Kind, contract.KindMirrorStale)
	}
	if f.Code != "mirror_stale" {
		t.Errorf("stale finding Code = %q, want %q", f.Code, "mirror_stale")
	}
	if f.Severity != doctor.SeverityWarning {
		t.Errorf("mirror staleness must be a WARNING (exit-neutral), got severity %q", f.Severity)
	}
	if f.Repo != "api" {
		t.Errorf("stale finding must carry the stale repo's identity, got repo=%q", f.Repo)
	}
}

// TestDetectMirrorStalenessIsWarningNeverExit6 is the §7.5 keystone, composed:
// because a stale mirror is a WARNING, a workspace whose ONLY trouble is a stale
// mirror still yields a clean exit 0 from WorstExit — doctor NEVER exits 6 (the
// mirror_stale → ExitLocked mapping) on staleness, and NEVER refreshes. Only the
// land path (HEAL-6) refuses on a stale mirror; reporting health does not.
//
// Registered non-vacuity mutant (DOCTOR-MIRROR, warning limb): changing the
// finding's Severity from SeverityWarning to SeverityError makes the stale
// mirror move the exit code, so WorstExit over a stale inventory returns
// ExitLocked (6) instead of ExitOK — reddening this test and the severity
// assertion above while the "exactly one finding" count stays green. This pins
// that staleness is advisory, never a refusal, in doctor.
func TestDetectMirrorStalenessIsWarningNeverExit6(t *testing.T) {
	none := doctor.WorstExit(doctor.DetectMirrorStaleness(nil))
	if none != contract.ExitOK {
		t.Errorf("an empty inventory is a clean diagnosis, got exit %d", none)
	}

	stale := doctor.DetectMirrorStaleness([]mirror.Snapshot{{Repo: "api", BehindOriginAsOfFetch: 5}})
	if got := doctor.WorstExit(stale); got != contract.ExitOK {
		t.Errorf("a stale mirror must be exit-neutral (clean exit %d), got exit %d", contract.ExitOK, got)
	}
}

// TestDetectMirrorStalenessFlagsOnlyStale isolates the staleness selection: the
// detector must report ONLY repos behind origin, never the fresh ones. An
// all-fresh inventory (including the zero-value Snapshot, which is fresh) yields
// zero findings.
//
// Registered non-vacuity mutant (DOCTOR-MIRROR, selection limb): dropping the
// negation in the filter
//
//	if !s.Freshness().Stale { continue }
//
// to `if s.Freshness().Stale { continue }` (skip the stale ones, flag the fresh)
// makes the fresh repos produce spurious findings and the stale ones vanish →
// this test RED (a finding appears where none should), while the genuine-stale
// row's count stays green — pinning that doctor selects mirror's specific
// "behind origin" verdict, not its negation.
func TestDetectMirrorStalenessFlagsOnlyStale(t *testing.T) {
	fresh := []mirror.Snapshot{
		{Repo: "api", BehindOriginAsOfFetch: 0},
		{Repo: "web", BehindOriginAsOfFetch: 0},
		{}, // zero-value snapshot is fresh (behind == 0)
	}
	if got := doctor.DetectMirrorStaleness(fresh); len(got) != 0 {
		t.Fatalf("no fresh mirror may be flagged as stale, got %d findings: %+v", len(got), got)
	}
}
