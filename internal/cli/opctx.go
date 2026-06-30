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

// dryRunKey is the unexported context key for the per-invocation --dry-run flag.
type dryRunKey struct{}

// WithDryRun returns a copy of ctx carrying whether this invocation is a --dry-run. Execute
// injects it (from Meta.DryRun) alongside the op_id, so a handler that must SPLIT a
// read-only plan from a mutating action (isolate repair: Inspect-and-report vs Repair) reads
// the same flag the envelope reports rather than re-deriving it. It rides the context for
// the same reason the op_id does — per-invocation runtime context the minimal Command.Run
// signature carries without growing. Most handlers ignore it (they have no plan/act split);
// the dry-run exit-neutrality (SHAPE-DRYRUN-EXIT0) lives in ExitFor, not here.
func WithDryRun(ctx context.Context, dryRun bool) context.Context {
	return context.WithValue(ctx, dryRunKey{}, dryRun)
}

// DryRunFrom returns whether Execute marked this invocation --dry-run, or false if unset
// (e.g. a handler driven directly in a test). A handler defaults to its mutating path when
// false, exactly as the CLI does when --dry-run is absent.
func DryRunFrom(ctx context.Context) bool {
	dr, _ := ctx.Value(dryRunKey{}).(bool)
	return dr
}
