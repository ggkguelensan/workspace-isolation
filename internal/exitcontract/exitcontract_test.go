package exitcontract_test

import (
	"sort"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/exitcontract"
)

// Guard SHAPE-FAIL-MATRIX: every closed error.kind maps to exactly the exit code
// the DESIGN §3.2 failure matrix pins, and the mapping is TOTAL over the contract's
// closed ErrorKind vocabulary (no kind unmapped, no extra). The expected pairing is
// an INDEPENDENT hand-written literal copy of the §3.2 table (the SHAPE-ENUM-DOUBLE-
// ENTRY pattern), so a mis-wired mapping in exitcontract.go disagrees with this
// second source and reddens. Agents branch on (kind, exit); a wrong pairing would
// silently mis-route every failure of that kind.
//
// Non-vacuity mutants (registered):
//   - Perturb one pairing (e.g. KindLockHeld -> ExitRefused/4 instead of ExitLocked/6)
//     => TestExitCodeForMatchesFailureMatrix RED on that kind's row.
//   - Drop a kind from the table (e.g. remove KindInternal) => the defensive default
//     (ExitInternal) still yields the right *value* for that one kind, so the value
//     test stays GREEN — but TestExitCodeForIsTotalOverAllKinds RED, because the
//     dropped kind is no longer in MappedKinds(). This is exactly why the totality
//     check is separate from the value check.
func wantExitByKind() map[contract.ErrorKind]contract.ExitCode {
	// Independent literal transcription of DESIGN.md §3.2:
	//   2 partial · 3 not_found · 4 dirty_worktree/conflict/already_exists ·
	//   5 needs_approval · 6 lock_held/mirror_stale(land) · 64 usage · 70 internal.
	// remote_error has no dedicated code in the closed set -> catch-all 70 (decision #X).
	return map[contract.ErrorKind]contract.ExitCode{
		contract.KindUsage:         contract.ExitUsage,       // 64
		contract.KindNotFound:      contract.ExitNotFound,    // 3
		contract.KindDirtyWorktree: contract.ExitRefused,     // 4
		contract.KindConflict:      contract.ExitRefused,     // 4
		contract.KindLockHeld:      contract.ExitLocked,      // 6
		contract.KindMirrorStale:   contract.ExitLocked,      // 6 (land path)
		contract.KindNeedsApproval: contract.ExitNeedsApprov, // 5
		contract.KindAlreadyExists: contract.ExitRefused,     // 4
		contract.KindPartial:       contract.ExitPartial,     // 2
		contract.KindRemoteError:   contract.ExitInternal,    // 70 (no dedicated code)
		contract.KindInternal:      contract.ExitInternal,    // 70
	}
}

func TestExitCodeForMatchesFailureMatrix(t *testing.T) {
	want := wantExitByKind()
	for kind, code := range want {
		if got := exitcontract.ExitCodeFor(kind); got != code {
			t.Errorf("ExitCodeFor(%q) = %d, want %d", kind, got, code)
		}
	}
}

// The mapping covers EXACTLY the contract's closed ErrorKind set — every kind is
// explicitly mapped (not silently defaulted), and the table has no kind the contract
// doesn't declare. This is the check that catches a dropped kind whose code happens
// to collide with the defensive default.
func TestExitCodeForIsTotalOverAllKinds(t *testing.T) {
	got := append([]contract.ErrorKind(nil), exitcontract.MappedKinds()...)
	want := append([]contract.ErrorKind(nil), contract.AllErrorKinds()...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })

	if len(got) != len(want) {
		t.Fatalf("MappedKinds() has %d kinds %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("MappedKinds()[%d] = %q, want %q (full got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}

	// And every code the table produces is itself a member of the closed ExitCode set.
	valid := make(map[contract.ExitCode]bool, len(contract.AllExitCodes()))
	for _, c := range contract.AllExitCodes() {
		valid[c] = true
	}
	for _, k := range contract.AllErrorKinds() {
		if c := exitcontract.ExitCodeFor(k); !valid[c] {
			t.Errorf("ExitCodeFor(%q) = %d, not a member of AllExitCodes()", k, c)
		}
	}
}

// An unknown kind (e.g. a future kind added to contract but not yet mapped, or a raw
// string that bypassed the typed enum) fails SAFE to the internal-error code rather
// than panicking or returning a bogus success.
func TestExitCodeForUnknownKindFailsToInternal(t *testing.T) {
	if got := exitcontract.ExitCodeFor(contract.ErrorKind("bogus_not_a_kind")); got != contract.ExitInternal {
		t.Errorf("ExitCodeFor(unknown) = %d, want ExitInternal (%d)", got, contract.ExitInternal)
	}
}
