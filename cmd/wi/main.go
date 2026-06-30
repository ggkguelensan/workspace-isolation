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
	"github.com/ggkguelensan/workspace-isolation/internal/recovery"
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

	// HEAL-4 (DESIGN §7.4, sub-unit 3d-iv): one OFFLINE roll-forward recovery pass before
	// any command body, so an operation a previous run interrupted past its commit point is
	// FINISHED deterministically at startup (and a never-committed one abandoned). Gated on
	// an initialized workspace — an uninitialized directory, and `wi init` itself, has no
	// .wi/ to recover and must not attempt it (recovery.Run needs .wi/locks). The pass is a
	// quiet self-heal: it emits NO envelope of its own (the one-envelope contract, §3.1) —
	// a successful roll-forward is evident in the resulting state, and a roll-forward that
	// FAILS leaves its journal in place for `wi doctor`'s pending-journal detector (§7.5),
	// the designated loud surface. Only a HARD fault (a non-HeldError lock failure or a
	// fatal journal/filesystem scan fault) is returned here: the workspace is then unsafe to
	// act on, so startup aborts with the single internal-error envelope rather than running
	// the command over a broken state. A contended pass (another process mid-recovery) and a
	// per-op Finisher error are NOT faults — they are absorbed inside recovery.Run.
	if workspaceInitialized(root) {
		if _, err := recovery.Run(ctx, root, deps.Git); err != nil {
			return startupFailure(stdout, clk, "startup recovery: "+err.Error())
		}
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

// workspaceInitialized reports whether root holds an initialized wi workspace — i.e. the
// .wi/ runtime subtree exists as a directory. It gates the startup recovery pass so an
// uninitialized directory (and `wi init`, which CREATES .wi/) is never recovered: there is
// nothing to recover, and recovery.Run's lock.Acquire would fail over a missing .wi/locks.
// os.Stat is a local syscall, never a network dial.
func workspaceInitialized(l layout.Layout) bool {
	info, err := os.Stat(l.WiDir())
	return err == nil && info.IsDir()
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
