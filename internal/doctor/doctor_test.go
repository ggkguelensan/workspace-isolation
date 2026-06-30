package doctor_test

import (
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/doctor"
)

// err/warn build a Finding of the given Kind at each severity — only Kind and
// Severity matter to WorstExit, so the other fields are left zero.
func err(k contract.ErrorKind) doctor.Finding {
	return doctor.Finding{Kind: k, Severity: doctor.SeverityError}
}
func warn(k contract.ErrorKind) doctor.Finding {
	return doctor.Finding{Kind: k, Severity: doctor.SeverityWarning}
}

// TestWorstExit pins "Exit = the worst finding's code" (DESIGN §7.5): the run's
// exit is the most severe ERROR finding's §3.2 code under the #DOCTOR-EXIT-WORST
// precedence, with WARNING findings exit-neutral. The rows that matter:
//
//   - no findings, and warnings-only, both exit 0 (a diagnosis that found nothing
//     to refuse on is a SUCCESS — the warnings still ride in the envelope);
//   - a worse error eclipses a milder one (internal > lock_held > refused > partial);
//   - a warning never lifts the exit even alongside errors (its code is ignored).
func TestWorstExit(t *testing.T) {
	cases := []struct {
		name     string
		findings []doctor.Finding
		want     contract.ExitCode
	}{
		{"nil", nil, contract.ExitOK},
		{"empty", []doctor.Finding{}, contract.ExitOK},

		// Warnings-only is a clean exit 0 — the §7.5 keystone: a stale mirror is a
		// WARNING and must never make doctor exit 6.
		{"warning-only-mirror-stale", []doctor.Finding{warn(contract.KindMirrorStale)}, contract.ExitOK},
		{"many-warnings", []doctor.Finding{warn(contract.KindMirrorStale), warn(contract.KindPartial)}, contract.ExitOK},

		// A single error → its own §3.2 code.
		{"single-conflict", []doctor.Finding{err(contract.KindConflict)}, contract.ExitRefused},
		{"single-partial", []doctor.Finding{err(contract.KindPartial)}, contract.ExitPartial},
		{"single-internal", []doctor.Finding{err(contract.KindInternal)}, contract.ExitInternal},
		{"single-lock-held", []doctor.Finding{err(contract.KindLockHeld)}, contract.ExitLocked},

		// The worst error wins, regardless of order.
		{"partial-then-lock-then-conflict", []doctor.Finding{
			err(contract.KindPartial), err(contract.KindLockHeld), err(contract.KindConflict),
		}, contract.ExitLocked},
		{"conflict-then-partial", []doctor.Finding{
			err(contract.KindConflict), err(contract.KindPartial),
		}, contract.ExitRefused},
		{"internal-eclipses-partial", []doctor.Finding{
			err(contract.KindPartial), err(contract.KindInternal),
		}, contract.ExitInternal},

		// A warning rides alongside errors but never lifts the exit: the worst
		// ERROR (conflict, exit 4) wins, NOT the warning's mapped code (6).
		{"warning-plus-errors", []doctor.Finding{
			warn(contract.KindMirrorStale), err(contract.KindPartial), err(contract.KindConflict),
		}, contract.ExitRefused},

		// Precedence is SEVERITY, not numeric: a lock_held error (exit 6) outranks
		// a usage error (exit 64). Numeric-max would wrongly pick 64.
		{"lock-held-outranks-usage", []doctor.Finding{
			err(contract.KindUsage), err(contract.KindLockHeld),
		}, contract.ExitLocked},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := doctor.WorstExit(tc.findings); got != tc.want {
				t.Errorf("WorstExit(%v) = %d, want %d", tc.findings, got, tc.want)
			}
		})
	}
}

// TestWorstExitWarningsNeverFail isolates the §7.5 keystone as a property: NO set
// of warning-only findings, of any Kind, may produce a non-zero exit. doctor's
// whole point is to report freely without refusing on advisory conditions.
//
// Registered non-vacuity mutant (DOCTOR-WORST-WARN-NEUTRAL): deleting the
//
//	if f.Severity != SeverityError { continue }
//
// skip in WorstExit folds warnings into the worst-of computation, so a warning of
// any error-mapped Kind (e.g. mirror_stale → exit 6) lifts the exit above OK and
// reddens both this test and the warning-only / warning-plus-errors rows above.
func TestWorstExitWarningsNeverFail(t *testing.T) {
	for _, k := range contract.AllErrorKinds() {
		if got := doctor.WorstExit([]doctor.Finding{warn(k)}); got != contract.ExitOK {
			t.Errorf("a warning of kind %q must be exit-neutral, got exit %d", k, got)
		}
	}
}

// TestWorstExitPrecedenceIsSeverityNotNumeric isolates #DOCTOR-EXIT-WORST: when
// two errors carry codes whose severity rank DISAGREES with their numeric order,
// WorstExit follows severity. lock_held maps to exit 6 and usage to exit 64; the
// lock is the worse health verdict, so the run exits 6 — never the numerically
// larger 64.
//
// Registered non-vacuity mutant (DOCTOR-WORST-PRECEDENCE): replacing moreSevere's
// rank comparison with a numeric `return a > b` makes usage (64) eclipse lock_held
// (6), returning exit 64 and reddening this test — while the all-numeric-monotone
// rows (internal > lock_held > refused > partial) stay green, isolating the limb.
func TestWorstExitPrecedenceIsSeverityNotNumeric(t *testing.T) {
	got := doctor.WorstExit([]doctor.Finding{
		err(contract.KindUsage),    // exit 64 — larger number
		err(contract.KindLockHeld), // exit 6  — worse health verdict
	})
	if got != contract.ExitLocked {
		t.Fatalf("severity precedence must rank lock_held (6) above usage (64); got exit %d, want %d", got, contract.ExitLocked)
	}
}
