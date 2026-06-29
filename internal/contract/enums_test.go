package contract

import (
	"slices"
	"testing"
)

// SHAPE-ENUM-DOUBLE-ENTRY (fitness, level: contract)
//
// Guards: the closed vocabularies (action, error.kind, exit codes, capabilities)
// against silent drift. Each set in enums.go is mirrored here by an INDEPENDENT
// literal copy. Widening or reordering a set in enums.go without the matching
// edit here fails CI — so changing a closed set requires two reviewed edits and
// (per the lock-file guard) a schema bump. See IMPLEMENTATION_PLAN.md §1.
//
// Non-vacuity (guard→mutant): adding a value to any All*() in enums.go without
// adding it to the corresponding want*() below turns the matching test RED.
// TestDoubleEntryIsNonVacuous proves the comparison genuinely detects a
// divergence, so a passing suite cannot be a vacuously-true one.

func wantActions() []string {
	return []string{"created", "removed", "synced", "landed", "read", "noop"}
}

func wantErrorKinds() []string {
	return []string{
		"usage", "not_found", "dirty_worktree", "conflict", "lock_held",
		"mirror_stale", "needs_approval", "already_exists", "partial",
		"remote_error", "internal",
	}
}

func wantExitCodes() []int {
	return []int{0, 2, 3, 4, 5, 6, 64, 70, 130}
}

func wantCapabilities() []string {
	return []string{
		"help-json", "resolve-block", "dry-run", "partial-success",
		"land", "land-atomic", "state-kv", "ports", "hooks", "remote-discovery",
	}
}

func wantWarningCodes() []string {
	return []string{"hydrate_skipped", "base_behind_ssot"}
}

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

func TestActionDoubleEntry(t *testing.T) {
	if got, want := strs(AllActions()), wantActions(); !slices.Equal(got, want) {
		t.Errorf("Action vocabulary drift:\n got = %v\n want = %v", got, want)
	}
}

func TestErrorKindDoubleEntry(t *testing.T) {
	if got, want := strs(AllErrorKinds()), wantErrorKinds(); !slices.Equal(got, want) {
		t.Errorf("ErrorKind vocabulary drift:\n got = %v\n want = %v", got, want)
	}
}

func TestExitCodeDoubleEntry(t *testing.T) {
	got := make([]int, len(AllExitCodes()))
	for i, c := range AllExitCodes() {
		got[i] = int(c)
	}
	if want := wantExitCodes(); !slices.Equal(got, want) {
		t.Errorf("ExitCode set drift:\n got = %v\n want = %v", got, want)
	}
}

func TestCapabilityDoubleEntry(t *testing.T) {
	if got, want := strs(AllCapabilities()), wantCapabilities(); !slices.Equal(got, want) {
		t.Errorf("Capability vocabulary drift:\n got = %v\n want = %v", got, want)
	}
}

func TestWarningCodeDoubleEntry(t *testing.T) {
	if got, want := strs(AllWarningCodes()), wantWarningCodes(); !slices.Equal(got, want) {
		t.Errorf("WarningCode vocabulary drift:\n got = %v\n want = %v", got, want)
	}
}

// TestNoDuplicateEnumValues guards against a copy-paste that repeats a value
// (which the ordered double-entry check alone could miss if want*() repeated it too).
func TestNoDuplicateEnumValues(t *testing.T) {
	assertUnique(t, "Action", strs(AllActions()))
	assertUnique(t, "ErrorKind", strs(AllErrorKinds()))
	assertUnique(t, "Capability", strs(AllCapabilities()))
	assertUnique(t, "WarningCode", strs(AllWarningCodes()))
}

func assertUnique(t *testing.T, name string, vals []string) {
	t.Helper()
	seen := make(map[string]bool, len(vals))
	for _, v := range vals {
		if seen[v] {
			t.Errorf("%s vocabulary has duplicate value %q", name, v)
		}
		seen[v] = true
	}
}

// TestDoubleEntryIsNonVacuous is the inline non-vacuity proof for the
// double-entry guard: a mutated copy with an extra value MUST compare unequal.
// If this ever passes the equality check, the guard is vacuous and the suite
// must fail loudly rather than give false confidence.
func TestDoubleEntryIsNonVacuous(t *testing.T) {
	base := strs(AllErrorKinds())
	mutated := append(slices.Clone(base), "timeout") // a value not in the closed set
	if slices.Equal(base, mutated) {
		t.Fatal("double-entry comparison is vacuous: it failed to detect an added enum value")
	}

	reordered := slices.Clone(base)
	if len(reordered) >= 2 {
		reordered[0], reordered[1] = reordered[1], reordered[0]
		if slices.Equal(base, reordered) {
			t.Fatal("double-entry comparison is vacuous: it failed to detect a reordering")
		}
	}
}

// TestCapabilitiesIsSubsetOfVocabulary enforces the "capability ⇒ backing
// command" invariant: every runtime-advertised capability is a member of the
// closed vocabulary. (The reverse need not hold — unwired capabilities exist
// in the vocabulary but are not advertised until their milestone.)
func TestCapabilitiesIsSubsetOfVocabulary(t *testing.T) {
	vocab := make(map[Capability]bool, len(AllCapabilities()))
	for _, c := range AllCapabilities() {
		vocab[c] = true
	}
	for _, c := range Capabilities() {
		if !vocab[c] {
			t.Errorf("advertised capability %q is not in the closed vocabulary", c)
		}
	}
}
