package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

// SHAPE-ENVELOPE-INVARIANTS (fitness, level: contract)
//
// Guards the two contractual shape invariants of every envelope:
//   - "error" is always present (null on success), never omitted.
//   - "repos" (and capabilities/warnings/next) is always an array, never null.
// Plus the locked top-level field order, frozen as golden bytes.
//
// Non-vacuity (guard→mutant): adding `,omitempty` to the Error field, or letting
// Repos marshal as null (removing the nil→[] coercion in MarshalJSON), turns
// TestEnvelopeErrorAlwaysPresent / TestEnvelopeReposAlwaysArray RED.
// TestFieldOrderCheckerIsNonVacuous proves the order extractor detects a reorder.

const goldenSuccess = `{"schema_version":"1.0","capabilities":["help-json","resolve-block","dry-run","partial-success"],"op_id":"op_test","command":"isolate new","ok":true,"action":"created","dry_run":false,"repos":[],"warnings":[],"next":[],"error":null}`

const goldenError = `{"schema_version":"1.0","capabilities":["help-json"],"op_id":"op_test","command":"isolate new","ok":false,"action":"noop","dry_run":false,"repos":[],"warnings":[],"next":[],"error":{"kind":"already_exists","message":"isolate exists"}}`

// goldenHelp is the `wi help sync` shape: a success envelope whose additive `help` block
// (decision #HB) carries the topic/synopsis/usage, with the model's runnable follow-ups
// riding the top-level next[]. It pins that `help` marshals between `next` and `error`
// (struct declaration order) and that the help-json capability has a real wire payload.
// The angle brackets in next[] marshal HTML-escaped (</>) — the same compact
// json.Marshal path Emit uses — which JSON consumers unescape transparently; this golden
// freezes that honest wire form.
const goldenHelp = `{"schema_version":"1.0","capabilities":["help-json"],"op_id":"op_test","command":"help","ok":true,"action":"read","dry_run":false,"repos":[],"warnings":[],"next":["wi isolate new \u003ctask\u003e \u003crepo\u003e…"],"help":{"topic":"sync","synopsis":"fetch every registered repo into its local mirror","usage":"wi sync"},"error":null}`

func successEnvelope() Envelope {
	return Envelope{
		SchemaVersion: SchemaVersion,
		Capabilities:  Capabilities(),
		OpID:          "op_test",
		Command:       "isolate new",
		OK:            true,
		Action:        ActionCreated,
		// Repos / Warnings / Next left nil on purpose — must marshal as [].
	}
}

func TestEnvelopeGoldenSuccess(t *testing.T) {
	b, err := json.Marshal(successEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != goldenSuccess {
		t.Errorf("success envelope drift:\n got = %s\nwant = %s", got, goldenSuccess)
	}
}

func TestEnvelopeGoldenError(t *testing.T) {
	e := Envelope{
		SchemaVersion: SchemaVersion,
		Capabilities:  []Capability{CapHelpJSON},
		OpID:          "op_test",
		Command:       "isolate new",
		OK:            false,
		Action:        ActionNoop,
		Error:         &Error{Kind: KindAlreadyExists, Message: "isolate exists"},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != goldenError {
		t.Errorf("error envelope drift:\n got = %s\nwant = %s", got, goldenError)
	}
}

// helpEnvelope is the typed source of goldenHelp.
func helpEnvelope() Envelope {
	return Envelope{
		SchemaVersion: SchemaVersion,
		Capabilities:  []Capability{CapHelpJSON},
		OpID:          "op_test",
		Command:       "help",
		OK:            true,
		Action:        ActionRead,
		Next:          []string{"wi isolate new <task> <repo>…"},
		Help: &HelpBlock{
			Topic:    "sync",
			Synopsis: "fetch every registered repo into its local mirror",
			Usage:    "wi sync",
		},
	}
}

// TestEnvelopeHelpBlockGolden freezes the wire bytes of a help-bearing envelope: the
// additive `help` block's field set/order and its position (between next and error).
// Non-vacuity mutant: reorder/rename a HelpBlock json tag, or move the Help struct field,
// and these bytes drift → RED.
func TestEnvelopeHelpBlockGolden(t *testing.T) {
	b, err := json.Marshal(helpEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != goldenHelp {
		t.Errorf("help envelope drift:\n got = %s\nwant = %s", got, goldenHelp)
	}
}

// TestEnvelopeHelpOmittedWhenNil pins the additive invariant: the `help` block is
// omitempty, so a non-help envelope (Help nil) carries NO "help" key — agents only see it
// when there is help to show. Non-vacuity mutant: drop `,omitempty` from Envelope.Help →
// "help":null appears on every envelope → this test (and the success/error goldens) RED.
func TestEnvelopeHelpOmittedWhenNil(t *testing.T) {
	b, _ := json.Marshal(successEnvelope()) // Help is nil
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["help"]; ok {
		t.Errorf(`"help" key present on a non-help envelope; the block must be omitempty, got %s`, b)
	}
}

func TestEnvelopeErrorAlwaysPresent(t *testing.T) {
	b, _ := json.Marshal(successEnvelope()) // Error is nil
	if !strings.Contains(string(b), `"error":null`) {
		t.Errorf("success envelope must contain explicit \"error\":null, got %s", b)
	}
	// And it must round-trip with the key present.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["error"]; !ok {
		t.Error(`"error" key absent — it must never be omitted`)
	}
}

func TestEnvelopeReposAlwaysArray(t *testing.T) {
	b, _ := json.Marshal(successEnvelope()) // Repos is nil
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"repos", "capabilities", "warnings", "next"} {
		v, ok := raw[key]
		if !ok {
			t.Errorf("%q key absent", key)
			continue
		}
		if strings.TrimSpace(string(v)) == "null" {
			t.Errorf("%q marshalled as null; must always be an array", key)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(v)), "[") {
			t.Errorf("%q is not a JSON array: %s", key, v)
		}
	}
}

// TestEnvelopeFieldOrder freezes the locked top-level key order.
func TestEnvelopeFieldOrder(t *testing.T) {
	b, _ := json.Marshal(successEnvelope())
	want := []string{
		"schema_version", "capabilities", "op_id", "command", "ok",
		"action", "dry_run", "repos", "warnings", "next", "error",
	}
	got := topLevelKeyOrder(t, b)
	if len(got) != len(want) {
		t.Fatalf("key count: got %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field order drift at %d: got %q, want %q (full got=%v)", i, got[i], want[i], got)
		}
	}
}

// topLevelKeyOrder reads the depth-1 object keys in marshalled order.
func topLevelKeyOrder(t *testing.T, b []byte) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(b)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("expected opening brace, got %v err %v", tok, err)
	}
	var keys []string
	depth := 0
	for dec.More() || depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			if depth == 0 {
				keys = append(keys, v)
				// consume the value token(s)
				val, err := dec.Token()
				if err != nil {
					t.Fatal(err)
				}
				if d, ok := val.(json.Delim); ok && (d == '{' || d == '[') {
					depth++
				}
			}
		}
	}
	return keys
}

func TestFieldOrderCheckerIsNonVacuous(t *testing.T) {
	got := topLevelKeyOrder(t, []byte(`{"b":1,"a":2}`))
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("order extractor is broken/vacuous: got %v, want [b a]", got)
	}
}
