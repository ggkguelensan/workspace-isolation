package suggest_test

import (
	"reflect"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/suggest"
)

// commands mirrors the v0 registered command set (the real candidates the dispatcher
// will hand to suggest.For). Kept local so this fitness never depends on cli wiring.
var commands = []string{"init", "resolve", "isolate new", "isolate rm", "sync", "repo add"}

// TestForSuggestsClosest is the SUGGEST-DIDYOUMEAN fitness guard. It pins the engine's
// contract: typos within MaxDistance and case-insensitive prefixes qualify, results are
// ordered (distance asc, name asc), and a no-match (or empty) input yields nil so the
// omitempty did_you_mean[] disappears. Non-vacuity mutant: make For return nil (the
// shipped stub) — or return its candidates unfiltered — and the "nothing close"/typo
// cases redden.
func TestForSuggestsClosest(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"single typo (transposition)", "reslove", []string{"resolve"}},
		{"single typo (missing letter)", "snc", []string{"sync"}},
		{"prefix matches several, ordered by distance", "re", []string{"resolve", "repo add"}},
		{"nothing close yields nil", "xyzzy", nil},
		{"empty input is never a suggestion", "", nil},
		{"matching is case-insensitive", "RESLOVE", []string{"resolve"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := suggest.For(tc.input, commands)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("For(%q) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}
