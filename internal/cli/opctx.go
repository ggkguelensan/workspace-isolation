package cli

import "context"

// opIDKey is the unexported context key for the per-invocation op_id; an unexported
// zero-size struct type guarantees no collision with any other package's context keys.
type opIDKey struct{}

// WithOpID returns a copy of ctx carrying the per-invocation op_id. Execute injects it
// before running a Command, so a handler that records the op identity in durable state
// (isolate new → IsolateRecord.OpID) reads the SAME id the envelope reports rather than
// minting a divergent one. The Command interface stays minimal (Run takes only a
// context); the op_id is per-invocation runtime context, exactly what a context carries.
func WithOpID(ctx context.Context, opID string) context.Context {
	return context.WithValue(ctx, opIDKey{}, opID)
}

// OpIDFrom returns the op_id Execute injected, or "" if none is present (e.g. a handler
// driven directly in a test, outside the pipeline). Within the real pipeline it is
// always set; a handler that needs it should still tolerate "" defensively.
func OpIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(opIDKey{}).(string)
	return id
}
