package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/resolve"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// newResolveCommand is the `wi resolve <task>` factory: it validates the positional
// args (exactly one task, a safe path segment) and binds the task + layout into a
// runnable Command. An arg error is a *CommandError{Kind: usage} (→ exit 64); the
// traversal check happens HERE so a bad task name is a clean usage refusal rather than
// surfacing later as an opaque internal error from state/layout.
func newResolveCommand(l layout.Layout, args []string) (Command, error) {
	if len(args) != 1 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi resolve <task>"}
	}
	task := args[0]
	if err := layout.ValidateSegment("task", task); err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	return &resolveCmd{layout: l, task: task}, nil
}

// resolveCommand answers "where is everything for this task?" as a PURE projection
// (DESIGN §3.1, §map line 166): load the task's durable state record and project it
// through resolve.Bundle into the resolve path-bundle. No git, no network, no mutation
// — it only reads the registry and joins paths. A never-created task surfaces as
// state.ErrNoRecord, which maps to a not_found refusal (the operator hint points at the
// command that would create it); any other load error is unclassified → internal.
type resolveCmd struct {
	layout layout.Layout
	task   string
}

func (c *resolveCmd) Run(ctx context.Context) (*Result, error) {
	rec, err := state.Load(c.layout.StateDir(), c.task)
	if errors.Is(err, state.ErrNoRecord) {
		return nil, &CommandError{
			Kind:    contract.KindNotFound,
			Message: fmt.Sprintf("no isolate %q", c.task),
			Help:    fmt.Sprintf("create it with: wi isolate new %s <repo>…", c.task),
		}
	}
	if err != nil {
		return nil, err
	}

	block, err := resolve.Bundle(c.layout, rec)
	if err != nil {
		return nil, err
	}
	return &Result{Action: contract.ActionRead, Resolve: &block}, nil
}
