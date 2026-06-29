package fault

import "testing"

// Unit guard for the fault seam's parser. The substring-non-match case is the
// non-vacuity pin: a mutant that used strings.Contains instead of exact-per-entry
// matching would activate "foo" on WI_FAULT=foobar and flip that case RED.
func TestActiveIn(t *testing.T) {
	cases := []struct {
		env, id string
		want    bool
	}{
		{"", "x", false},         // empty env: nothing active
		{"x", "", false},         // empty id: never active
		{"a", "a", true},         // exact single match
		{"a,b,c", "b", true},     // match among many
		{" a , b ", "b", true},   // surrounding whitespace trimmed
		{"foobar", "foo", false}, // EXACT only — substring must not match
		{"foo", "foobar", false}, // and not the other direction either
		{"a,b", "c", false},      // absent id
	}
	for _, c := range cases {
		if got := activeIn(c.env, c.id); got != c.want {
			t.Errorf("activeIn(%q, %q) = %v, want %v", c.env, c.id, got, c.want)
		}
	}
}
