package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/cli/opid"
	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// Registry maps a canonical command string (e.g. "init", "isolate new") to a factory
// that binds the command's positional args into a runnable Command. The real registry
// is built with each command's dependencies (layout/clock/git/…) captured in the
// closure, so Dispatch itself stays dependency-agnostic and the factory signature need
// only carry the parsed args. A factory returns an error when the args are invalid —
// Dispatch maps that to a `usage` envelope.
type Registry map[string]func(args []string) (Command, error)

// Dispatch is the front half of the Runner (DESIGN §3, §4). It parses argv into the
// global flags + the positional command + its args, resolves the named subcommand
// against reg, mints a fresh op_id from clk, builds Meta, and hands off to Execute. A
// parse error, an unknown command, or a factory that rejects its args all become a
// single `usage` envelope (kind=usage → exit 64) — every path still emits EXACTLY ONE
// envelope. clk supplies only the op_id's time + randomness; a command that needs a
// clock receives its own via the registry closure.
//
// Decision #F: the parser is hand-rolled on the stdlib (a tiny global-flag extractor +
// a longest-match command lookup), NOT cobra — consistent with wi's zero-dep posture
// (decisions #6, #C) and its small, fixed command surface. A returned Go error is
// reserved for an infrastructure write failure (propagated from Execute/emit).
func Dispatch(ctx context.Context, w io.Writer, clk clock.Clock, reg Registry, args []string) (contract.ExitCode, error) {
	// Mint the op_id first so every path — including the error paths below — carries a
	// correlation id. crypto/rand failing is catastrophic and vanishingly rare; surface
	// it as an internal error rather than panicking.
	opID, mintErr := opid.New(clk.Now(), clk.Rand())

	dryRun, format, positional, perr := parseGlobals(args)
	m := Meta{OpID: opID, Command: bestEffortName(positional), DryRun: dryRun}

	if mintErr != nil {
		return emit(w, Failure(m, contract.ActionNoop, contract.Error{
			Kind:    contract.KindInternal,
			Message: "mint op_id: " + mintErr.Error(),
		}), format)
	}
	if perr != nil {
		return emit(w, usageEnvelope(m, perr.Error()), format)
	}

	name, rest, ok := resolveCommand(reg, positional)
	if !ok {
		return emit(w, usageEnvelope(m, "unknown command: "+bestEffortName(positional)), format)
	}
	m.Command = name

	cmd, ferr := reg[name](rest)
	if ferr != nil {
		return emit(w, usageEnvelope(m, ferr.Error()), format)
	}
	return Execute(ctx, w, m, format, cmd)
}

// parseGlobals extracts the recognized global flags (--dry-run, --format json|text, and
// the --format=… form) from anywhere in args, returning everything else as positional.
// Globals are accepted in any position (agent-friendly) since v0 command args are simple
// names/URLs that never start with "--". format defaults to json. (A `--` end-of-flags
// terminator is a deferred enrichment.)
func parseGlobals(args []string) (dryRun bool, format Format, positional []string, err error) {
	format = FormatJSON
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--format":
			if i+1 >= len(args) {
				return false, FormatJSON, nil, fmt.Errorf("--format requires a value (json|text)")
			}
			i++
			f, ferr := parseFormat(args[i])
			if ferr != nil {
				return false, FormatJSON, nil, ferr
			}
			format = f
		case strings.HasPrefix(a, "--format="):
			f, ferr := parseFormat(strings.TrimPrefix(a, "--format="))
			if ferr != nil {
				return false, FormatJSON, nil, ferr
			}
			format = f
		default:
			positional = append(positional, a)
		}
	}
	return dryRun, format, positional, nil
}

func parseFormat(s string) (Format, error) {
	switch s {
	case "json":
		return FormatJSON, nil
	case "text":
		return FormatText, nil
	default:
		return "", fmt.Errorf("invalid --format %q (want json|text)", s)
	}
}

// resolveCommand finds the longest registered command name at the front of positional
// (up to two tokens, e.g. "isolate new"), returning the remaining tokens as the
// command's args. A two-token match wins over a one-token match so a group verb like
// "isolate" never shadows "isolate new".
func resolveCommand(reg Registry, positional []string) (name string, rest []string, ok bool) {
	if len(positional) >= 2 {
		two := positional[0] + " " + positional[1]
		if _, found := reg[two]; found {
			return two, positional[2:], true
		}
	}
	if len(positional) >= 1 {
		if _, found := reg[positional[0]]; found {
			return positional[0], positional[1:], true
		}
	}
	return "", nil, false
}

// bestEffortName labels an envelope whose command could not be resolved (parse error or
// unknown command): the first one or two positional tokens the user typed.
func bestEffortName(positional []string) string {
	switch len(positional) {
	case 0:
		return ""
	case 1:
		return positional[0]
	default:
		return positional[0] + " " + positional[1]
	}
}

// usageEnvelope builds the standard usage failure (kind=usage → exit 64).
func usageEnvelope(m Meta, msg string) contract.Envelope {
	return Failure(m, contract.ActionNoop, contract.Error{
		Kind:    contract.KindUsage,
		Message: msg,
	})
}

// emit renders env in the chosen format and returns its exit code — the same
// render+ExitFor wiring Execute uses, for the dispatch-side envelopes that never reach
// a Command.
func emit(w io.Writer, env contract.Envelope, format Format) (contract.ExitCode, error) {
	if err := Render(w, env, format); err != nil {
		return contract.ExitInternal, err
	}
	return ExitFor(env), nil
}
