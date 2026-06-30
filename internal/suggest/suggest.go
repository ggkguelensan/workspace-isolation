// Package suggest is wi's SOLE owner of the "did you mean" engine (DESIGN §3.1 /
// IMPLEMENTATION_PLAN M3): one hand-rolled Levenshtein distance plus the candidate
// selector the dispatcher uses to turn an unknown command into the envelope's
// did_you_mean[] hint. It is pure — no I/O, no dependencies — so the contract layer
// can call it deterministically and the golden envelopes stay byte-stable.
//
// Behavior intentionally reproduces the cobra SuggestionsFor algorithm wi would have
// inherited had it kept cobra (decision #F dropped cobra for a hand-rolled dispatcher,
// so DESIGN §7's "defer unknown-command typos to cobra's SuggestionsFor" is satisfied
// by replicating that behavior here, recorded as decision #S): a candidate qualifies
// when its case-insensitive Levenshtein distance to the input is ≤ MaxDistance, OR the
// candidate has the input as a case-insensitive prefix.
package suggest

import (
	"sort"
	"strings"
)

// MaxDistance is the inclusive Levenshtein threshold for treating a candidate as a
// typo of the input — cobra's historical default (2). A wider edit gap means "not a
// typo of this command", so it does not surface as a suggestion.
const MaxDistance = 2

// For returns the command names input most likely meant, best match first, or nil
// when nothing qualifies (so the omitempty did_you_mean[] field simply disappears).
// Matching is case-insensitive; results are sorted by (distance asc, name asc) for a
// deterministic, schema-stable hint. An empty input yields nil — every string is a
// prefix of "", which would suggest the whole command set and help no one.
func For(input string, candidates []string) []string {
	if input == "" {
		return nil
	}
	in := strings.ToLower(input)

	type match struct {
		name string
		dist int
	}
	var matches []match
	for _, c := range candidates {
		lc := strings.ToLower(c)
		d := distance(in, lc)
		if d <= MaxDistance || strings.HasPrefix(lc, in) {
			matches = append(matches, match{name: c, dist: d})
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].dist != matches[j].dist {
			return matches[i].dist < matches[j].dist
		}
		return matches[i].name < matches[j].name
	})
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.name
	}
	return out
}

// distance is wi's ONE Levenshtein edit distance (insert/delete/substitute, unit cost)
// between a and b, computed over runes with a two-row dynamic-programming table —
// O(len(a)·len(b)) time, O(len(b)) space. Callers pass already-lowercased strings.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}
