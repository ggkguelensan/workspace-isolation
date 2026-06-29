// Command wi is the workspace-isolation CLI entry point: the SINGLE main package and the
// SINGLE os.Exit site in the tree (via exitcontract.Exit, DESIGN §4). main() does nothing
// but call run and hand its code to that one exit wrapper, so all wiring stays in run —
// a testable seam that never terminates the process.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/cli/opid"
	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/exitcontract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

func main() {
	exitcontract.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// run is the production wiring of the Runner pipeline, factored out of main() so it is
// unit-testable without terminating the process. It (1) discovers the workspace root from
// the current directory (decision #G: root = cwd; init defines it, every other command
// resolves it), (2) builds the real dependencies — the resolved layout, the os/exec git
// driver (its egress belt enforces the no-hidden-network invariant, DESIGN §2.3), and the
// System clock — and the registry over them, then (3) hands argv to Dispatch, which emits
// EXACTLY ONE envelope to stdout and returns the mapped exit code. run propagates that
// code unchanged.
//
// Two failures live above Dispatch and so are handled here, each still emitting exactly
// one envelope: a root that cannot be resolved (a broken cwd — kind=internal, exit 70),
// and an infrastructure failure WRITING the envelope (Dispatch's Go-error return — there
// is no envelope to show, so it is reported on stderr, exit 70).
func run(ctx context.Context, args []string, stdout, stderr io.Writer) contract.ExitCode {
	clk := clock.System{}

	root, err := workspaceRoot()
	if err != nil {
		return startupFailure(stdout, clk, "resolve workspace root: "+err.Error())
	}

	deps := cli.Deps{
		Layout: root,
		Git:    git.New(gitexec.New()),
		Clock:  clk,
	}
	code, werr := cli.Dispatch(ctx, stdout, clk, cli.BuildRegistry(deps), args)
	if werr != nil {
		// The envelope could not be written; there is nothing well-formed on stdout to
		// carry the failure, so surface it on stderr (the one place a non-envelope line
		// is allowed) and exit internal.
		fmt.Fprintln(stderr, "wi: "+werr.Error())
		return contract.ExitInternal
	}
	return code
}

// workspaceRoot resolves the current directory into a normalized layout.Layout (decision
// #G). Both os.Getwd and layout.Resolve (filepath.Abs + EvalSymlinks) are local syscalls,
// never a network dial.
func workspaceRoot() (layout.Layout, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return layout.Layout{}, err
	}
	return layout.Resolve(cwd)
}

// startupFailure emits the single envelope for a fault that occurs before Dispatch can run
// (so the one-envelope contract holds even when the workspace root is unresolvable). It
// mints an op_id the same way Dispatch does so the failure still carries a correlation id,
// and defaults to JSON (the agent-facing default; no globals have been parsed yet).
func startupFailure(stdout io.Writer, clk clock.Clock, msg string) contract.ExitCode {
	opID, _ := opid.New(clk.Now(), clk.Rand())
	env := cli.Failure(cli.Meta{OpID: opID}, contract.ActionNoop, contract.Error{
		Kind:    contract.KindInternal,
		Message: msg,
	})
	_ = cli.Emit(stdout, env)
	return cli.ExitFor(env)
}
