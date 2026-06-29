package cli_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// Guard CTX-OPID: the per-invocation op_id reaches a Command through the context.
// Command.Run takes only a context (the interface stays minimal), yet a handler that
// records the op identity in durable state — isolate new writes IsolateRecord.OpID —
// must use the SAME id the envelope reports, not a divergent freshly-minted one. The
// seam is WithOpID/OpIDFrom, and the load-bearing property is that Execute INJECTS
// Meta.OpID into the ctx the Command observes (so the wiring, not just the helper, is
// proven). A bare context yields "" so an out-of-pipeline handler degrades cleanly.
//
// Non-vacuity mutant (registered): in Execute drop the `ctx = WithOpID(ctx, m.OpID)`
// injection → the Command observes "" instead of the minted op_id →
// TestExecuteInjectsOpIDIntoContext RED (observed "" , want the Meta op_id).

// capturingCommand records the op_id visible in the context its Run receives, so the
// guard can assert Execute threaded Meta.OpID down to the handler.
type capturingCommand struct {
	seen *string
}

func (c capturingCommand) Run(ctx context.Context) (*cli.Result, error) {
	*c.seen = cli.OpIDFrom(ctx)
	return &cli.Result{Action: contract.ActionNoop}, nil
}

func TestExecuteInjectsOpIDIntoContext(t *testing.T) {
	const opID = "op_test_correlation_id"
	var seen string
	m := cli.Meta{OpID: opID, Command: "noop", DryRun: false}

	var buf bytes.Buffer
	if _, err := cli.Execute(context.Background(), &buf, m, cli.FormatJSON, capturingCommand{seen: &seen}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen != opID {
		t.Errorf("Command saw op_id %q in its context, want %q (Execute must inject Meta.OpID)", seen, opID)
	}
}

func TestOpIDRoundTripsThroughContext(t *testing.T) {
	const opID = "op_round_trip"
	ctx := cli.WithOpID(context.Background(), opID)
	if got := cli.OpIDFrom(ctx); got != opID {
		t.Errorf("OpIDFrom = %q, want %q", got, opID)
	}
	// A bare context carries no op_id.
	if got := cli.OpIDFrom(context.Background()); got != "" {
		t.Errorf("OpIDFrom(bare ctx) = %q, want \"\"", got)
	}
}
