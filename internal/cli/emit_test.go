package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/schema"
)

// Guard SHAPE-ONE-ENVELOPE: a wi command emits EXACTLY ONE well-formed, schema-valid
// envelope per invocation, as a single compact line terminated by one newline, and
// nothing else on the stream. cli.Emit is the chokepoint that property is enforced
// at — it serializes via the SAME json.Marshal path the contract goldens are frozen
// against (so SHAPE-FINGERPRINT/SHAPE-SCHEMA stay the single source of truth for
// shape) and appends one '\n' for line-oriented consumers.
//
// Non-vacuity mutants (registered):
//   - Emit the envelope TWICE (a second w.Write) => TestEmitWritesExactlyOneEnvelope
//     RED: the stream now carries two top-level JSON values, so the second Decode
//     returns a document instead of io.EOF. This targets the core "exactly ONE
//     envelope" property.
//   - Drop the trailing newline (don't append '\n') => TestEmitTerminatesWithSingleNewline
//     RED: the output no longer ends in exactly one newline.

func successEnvelope() contract.Envelope {
	return contract.Envelope{
		SchemaVersion: contract.SchemaVersion,
		Capabilities:  contract.Capabilities(),
		OpID:          "op_test",
		Command:       "isolate new",
		OK:            true,
		Action:        contract.ActionCreated,
		// repos/warnings/next left nil — MarshalJSON coerces them to [].
		Error: nil,
	}
}

func errorEnvelope() contract.Envelope {
	return contract.Envelope{
		SchemaVersion: contract.SchemaVersion,
		Capabilities:  []contract.Capability{contract.CapHelpJSON},
		OpID:          "op_test",
		Command:       "isolate new",
		OK:            false,
		Action:        contract.ActionNoop,
		Error:         &contract.Error{Kind: contract.KindAlreadyExists, Message: "isolate exists"},
	}
}

func TestEmitWritesExactlyOneEnvelope(t *testing.T) {
	var buf bytes.Buffer
	if err := cli.Emit(&buf, successEnvelope()); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	dec := json.NewDecoder(&buf)
	var first map[string]any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("decoding the first envelope: %v", err)
	}

	// The stream carries EXACTLY one top-level value: the next decode is EOF.
	var second map[string]any
	if err := dec.Decode(&second); err != io.EOF {
		t.Fatalf("after one envelope the stream must be at EOF, got err=%v (second=%v)", err, second)
	}

	// The shape invariants survive emission: error key present (null on success),
	// and the four list fields are arrays, never null, so an agent can index blind.
	if _, ok := first["error"]; !ok {
		t.Error(`emitted envelope is missing the "error" key (must be present, null on success)`)
	}
	for _, key := range []string{"repos", "capabilities", "warnings", "next"} {
		if _, isArray := first[key].([]any); !isArray {
			t.Errorf("emitted %q = %T, want a JSON array (never null)", key, first[key])
		}
	}
}

func TestEmitOutputIsSchemaValid(t *testing.T) {
	sch := compileEnvelopeSchema(t)
	for name, env := range map[string]contract.Envelope{
		"success": successEnvelope(),
		"error":   errorEnvelope(),
	} {
		var buf bytes.Buffer
		if err := cli.Emit(&buf, env); err != nil {
			t.Fatalf("Emit %s: %v", name, err)
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("emitted %s envelope is not valid JSON: %v", name, err)
		}
		if err := sch.Validate(inst); err != nil {
			t.Errorf("emitted %s envelope fails the published schema: %v", name, err)
		}
	}
}

func TestEmitTerminatesWithSingleNewline(t *testing.T) {
	var buf bytes.Buffer
	if err := cli.Emit(&buf, successEnvelope()); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	out := buf.Bytes()

	if len(out) == 0 || out[len(out)-1] != '\n' {
		t.Fatalf("emitted output must end in a newline, got %q", out)
	}
	if out[len(out)-2] == '\n' {
		t.Errorf("emitted output ends in more than one newline, got %q", out[len(out)-3:])
	}
	// The payload is a single compact line — no embedded newline before the terminator.
	if body := out[:len(out)-1]; bytes.IndexByte(body, '\n') != -1 {
		t.Errorf("emitted envelope is not a single compact line: %q", body)
	}
}

// The emitter must reserialize through the SAME marshaller as the contract goldens,
// so the frozen wire bytes govern emitted output too — no Encoder/escaping drift.
func TestEmitMatchesContractMarshal(t *testing.T) {
	env := successEnvelope()
	want, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var buf bytes.Buffer
	if err := cli.Emit(&buf, env); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if got := bytes.TrimRight(buf.Bytes(), "\n"); !bytes.Equal(got, want) {
		t.Errorf("emit payload drifted from contract marshal:\n got = %s\nwant = %s", got, want)
	}
}

// compileEnvelopeSchema mirrors the contract package's validator harness so this
// black-box test validates emitted bytes against the published schema SSOT.
func compileEnvelopeSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.EnvelopeSchema))
	if err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("envelope.schema.json", doc); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	sch, err := c.Compile("envelope.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}
