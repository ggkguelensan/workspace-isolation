package cli

import (
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// Deps carries the already-resolved dependencies the command factories close over.
// The CLI builds it ONCE at startup — root discovery → a normalized layout.Layout,
// plus (as later handlers need them) a clock and a *git.Git — and BuildRegistry binds
// each into its command's factory, so a Command receives its deps pre-bound and Run
// needs only a context. This struct grows additively as handlers land (a new dep is a
// new field; an existing handler is untouched). Git is the shared git driver the
// materializing commands (isolate new, sync) need; read-only commands leave it nil.
type Deps struct {
	Layout layout.Layout
	Git    *git.Git
}

// BuildRegistry wires every wi subcommand's factory over deps, returning the Registry
// that cmd/wi hands to Dispatch. Each entry's key is the canonical command string
// Dispatch resolves argv against (longest-match, so a 2-token "isolate new" coexists
// with a 1-token "isolate"); each value parses+validates that command's positional
// args and returns a runnable Command (or a *CommandError{Kind: usage}). Adding a
// command is one line here plus its cmd_<name>.go handler — no change to Dispatch.
func BuildRegistry(d Deps) Registry {
	return Registry{
		"init":        func(args []string) (Command, error) { return newInitCommand(d.Layout, args) },
		"resolve":     func(args []string) (Command, error) { return newResolveCommand(d.Layout, args) },
		"isolate new": func(args []string) (Command, error) { return newIsolateNewCommand(d.Layout, d.Git, args) },
	}
}
