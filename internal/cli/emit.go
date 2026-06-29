// Package cli is wi's uniform command pipeline (DESIGN §4): it turns a command's
// typed domain Result into exactly one well-formed envelope on stdout and the
// matching process exit code, and it is the SOLE place envelopes are assembled,
// serialized, and exit codes computed (domain packages return plain structs and
// never touch the wire). The pieces land bottom-up; this file is the emitter — the
// final serialization step every command's output funnels through.
package cli

import (
	"encoding/json"
	"io"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// Emit writes env to w as EXACTLY ONE envelope: a single compact JSON line followed
// by one newline, and nothing else (DESIGN §3.1 — "exactly one per invocation, JSON
// by default"; SHAPE-ONE-ENVELOPE).
//
// It serializes through contract.Envelope's own json.Marshal path — the same bytes
// the contract goldens and the published schema are frozen against — so emitted
// output can never drift from the contractual wire shape (no alternate Encoder, no
// HTML-escaping divergence). Envelope.MarshalJSON has already guaranteed the
// always-present "error" and always-array repos/capabilities/warnings/next
// invariants; Emit only appends the trailing newline that makes the stream
// line-oriented (one envelope per line) and writes the result in a single call.
func Emit(w io.Writer, env contract.Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
