// Package schema embeds the published wire-contract JSON Schema so it can be
// served by `wi schema` and validated against in tests without cwd-relative
// path lookups. The JSON file is the SOURCE OF TRUTH for envelope shape; the
// typed contract.Envelope struct is built to satisfy it (DESIGN.md §3.1).
package schema

import _ "embed"

// EnvelopeSchema is the draft 2020-12 JSON Schema for the wi envelope.
//
//go:embed envelope.schema.json
var EnvelopeSchema []byte
