package cli

import (
	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// Deps carries the already-resolved dependencies the command factories close over.
// The CLI builds it ONCE at startup — root discovery → a normalized layout.Layout,
// plus a clock and a *git.Git — and BuildRegistry binds each into its command's factory,
// so a Command receives its deps pre-bound and Run needs only a context. This struct
// grows additively as handlers land (a new dep is a new field; an existing handler is
// untouched). Git is the shared git driver the materializing commands (isolate new, sync)
// need; read-only commands leave it nil. Clock supplies the wall-clock the network
// commands stamp into freshness snapshots (sync); commands that don't timestamp leave it
// nil (the CLI always wires clock.System, so this is nil only in narrow unit tests).
type Deps struct {
	Layout layout.Layout
	Git    *git.Git
	Clock  clock.Clock
}

// BuildRegistry wires every wi subcommand's factory over deps, returning the Registry
// that cmd/wi hands to Dispatch. Each entry's key is the canonical command string
// Dispatch resolves argv against (longest-match, so a 2-token "isolate new" coexists
// with a 1-token "isolate"); each value parses+validates that command's positional
// args and returns a runnable Command (or a *CommandError{Kind: usage}). Adding a
// command is one line here plus its cmd_<name>.go handler — no change to Dispatch.
func BuildRegistry(d Deps) Registry {
	r := Registry{
		"init":           func(args []string) (Command, error) { return newInitCommand(d.Layout, args) },
		"resolve":        func(args []string) (Command, error) { return newResolveCommand(d.Layout, args) },
		"isolate new":    func(args []string) (Command, error) { return newIsolateNewCommand(d.Layout, d.Git, args) },
		"isolate rm":     func(args []string) (Command, error) { return newIsolateRmCommand(d.Layout, d.Git, args) },
		"isolate repair": func(args []string) (Command, error) { return newIsolateRepairCommand(d.Layout, d.Git, args) },
		"land":           func(args []string) (Command, error) { return newLandCommand(d.Layout, d.Git, args) },
		"gc":             func(args []string) (Command, error) { return newGCCommand(d.Layout, d.Git, args) },
		"sync":           func(args []string) (Command, error) { return newSyncCommand(d.Layout, d.Git, d.Clock, args) },
		"repo add":       func(args []string) (Command, error) { return newRepoAddCommand(d.Layout, args) },
		"help":           func(args []string) (Command, error) { return newHelpCommand(args) },
	}
	// Platform-specific commands merge in here. The unix-only lock-self-heal surface
	// (lock ls / lock break) is contributed by lockCommands; on non-unix the stub adds none.
	for k, f := range lockCommands(d) {
		r[k] = f
	}
	return r
}
