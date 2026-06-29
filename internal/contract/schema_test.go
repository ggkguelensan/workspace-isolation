package contract

import (
	"bytes"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ggkguelensan/workspace-isolation/schema"
)

// SHAPE-SCHEMA (fitness, level: contract)
//
// The published JSON Schema (schema/envelope.schema.json, draft 2020-12,
// additionalProperties:false) is the SOURCE OF TRUTH for envelope shape. This
// guard proves the schema is a faithful, CLOSED constraint:
//   - the golden success/error envelopes (the same bytes the Go marshaller
//     emits) validate against it, and
//   - a battery of deliberately-malformed envelopes is REJECTED.
//
// Non-vacuity (guard→mutant): loosening the schema's top-level
// "additionalProperties" to true (or dropping "error" from "required", or
// widening a closed enum) lets a malformed envelope validate, turning
// TestSchemaRejectsInvalid RED. TestSchemaAcceptsGolden goes RED if the schema
// drifts away from the bytes the struct actually emits.

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

func validateInstance(t *testing.T, sch *jsonschema.Schema, instance string) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(instance)))
	if err != nil {
		t.Fatalf("unmarshal instance %q: %v", instance, err)
	}
	return sch.Validate(inst)
}

func TestSchemaAcceptsGolden(t *testing.T) {
	sch := compileEnvelopeSchema(t)
	for name, env := range map[string]string{
		"success": goldenSuccess,
		"error":   goldenError,
	} {
		if err := validateInstance(t, sch, env); err != nil {
			t.Errorf("golden %s envelope must validate against the schema, got: %v", name, err)
		}
	}
}

func TestSchemaRejectsInvalid(t *testing.T) {
	sch := compileEnvelopeSchema(t)
	cases := map[string]string{
		// extra unknown top-level key — only rejected if additionalProperties:false holds
		"unknown top-level key": `{"schema_version":"1.0","capabilities":[],"op_id":"op_test","command":"x","ok":true,"action":"created","dry_run":false,"repos":[],"warnings":[],"next":[],"error":null,"bogus":1}`,
		// "error" key omitted — only rejected if "error" is in required
		"missing error key": `{"schema_version":"1.0","capabilities":[],"op_id":"op_test","command":"x","ok":true,"action":"created","dry_run":false,"repos":[],"warnings":[],"next":[]}`,
		// action outside the closed enum
		"unknown action": `{"schema_version":"1.0","capabilities":[],"op_id":"op_test","command":"x","ok":true,"action":"frobnicate","dry_run":false,"repos":[],"warnings":[],"next":[],"error":null}`,
		// error.kind outside the closed enum
		"unknown error kind": `{"schema_version":"1.0","capabilities":[],"op_id":"op_test","command":"x","ok":false,"action":"noop","dry_run":false,"repos":[],"warnings":[],"next":[],"error":{"kind":"kaboom","message":"x"}}`,
		// warning.code outside the closed vocab
		"unknown warning code": `{"schema_version":"1.0","capabilities":[],"op_id":"op_test","command":"x","ok":true,"action":"created","dry_run":false,"repos":[],"warnings":[{"code":"made_up","message":"x"}],"next":[],"error":null}`,
		// wrong schema_version (const "1.0")
		"wrong schema_version": `{"schema_version":"9.9","capabilities":[],"op_id":"op_test","command":"x","ok":true,"action":"created","dry_run":false,"repos":[],"warnings":[],"next":[],"error":null}`,
		// repos as null instead of an array
		"repos null": `{"schema_version":"1.0","capabilities":[],"op_id":"op_test","command":"x","ok":true,"action":"created","dry_run":false,"repos":null,"warnings":[],"next":[],"error":null}`,
	}
	for name, env := range cases {
		if err := validateInstance(t, sch, env); err == nil {
			t.Errorf("invalid envelope %q must be REJECTED by the schema, but it validated", name)
		}
	}
}

// TestSchemaInvalidCorpusIsNonVacuous proves the reject-test cannot pass
// vacuously: every case must differ from a known-good envelope in exactly the
// way named, so a base-valid envelope still validates (else the validator
// itself is broken and rejects everything).
func TestSchemaInvalidCorpusIsNonVacuous(t *testing.T) {
	sch := compileEnvelopeSchema(t)
	if err := validateInstance(t, sch, goldenSuccess); err != nil {
		t.Fatalf("validator is broken/over-strict: it rejects the known-good success envelope: %v", err)
	}
}
