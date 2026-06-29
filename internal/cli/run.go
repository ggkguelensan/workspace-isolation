package cli

import (
	"context"
	"errors"
	"io"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// Result is the typed domain outcome a Command produces. The Runner maps it to a
// success Envelope via Success and threads the additive blocks onto it; a handler
// returns plain data and NEVER assembles the wire shape or picks an exit code. Every
// field mirrors an envelope field the command may populate (DESIGN §3.1) — Action and
// Repos are the common pair; Resolve/Planned/Blocked are the command-specific additive
// blocks; Warnings/Next are advisory.
type Result struct {
	Action   contract.Action
	Repos    []contract.RepoResult
	Resolve  *contract.ResolveBlock
	Planned  []contract.PlanItem
	Blocked  []contract.BlockItem
	Warnings []contract.Warning
	Next     []string
}

// Command is one wi subcommand. Run does the domain work over the green core and
// returns typed data — it is constructed by the dispatcher with its parsed args and
// dependencies already bound, so Run needs only a context (for cancellation / git
// child timeouts). The return convention the Runner relies on:
//   - (result, nil)        success
//   - (nil, *CommandError) a clean, classified failure (the error.kind selects the code)
//   - (nil, plainError)    an unclassified failure → kind=internal (a bug, surfaced)
//   - (result, *CommandError{Kind: partial})  a DURABLE PARTIAL: ok:false with a
//     top-level error.kind=partial AND per-repo detail carried from the result
//     (decision #D). This is the only case both returns are non-nil.
type Command interface {
	Run(ctx context.Context) (*Result, error)
}

// CommandError is the typed error a Command returns to choose a failure Envelope's
// error.kind and carry optional operator hints. A returned error that is NOT a
// *CommandError maps to kind=internal — an unclassified failure is treated as a bug and
// surfaced as such, never silently reshaped into a friendlier kind. Action is the verb
// in flight (e.g. created for a partial); it defaults to noop (a refusal acted on
// nothing).
type CommandError struct {
	Kind       contract.ErrorKind
	Message    string
	Repo       string
	Help       string
	DidYouMean []string
	Action     contract.Action
}

func (e *CommandError) Error() string { return e.Message }

// Format is the CLI presentation choice for the single emitted envelope. It is a
// presentation concern, not a wire value, so internal/cli owns it (the closed wire
// enums stay in internal/contract). json is the agent-facing default; text is the
// lossless human projection (RenderText).
type Format string

const (
	FormatJSON Format = "json"
	FormatText Format = "text"
)

// Render writes env to w in the chosen format: json (the default) goes through Emit
// (exactly one compact line + newline), text through RenderText (the lossless human
// projection). Both consume the SAME assembled struct, so the two wire forms can never
// disagree (DESIGN §3.1).
func Render(w io.Writer, env contract.Envelope, format Format) error {
	if format == FormatText {
		return RenderText(w, env)
	}
	return Emit(w, env)
}

// Execute runs one already-constructed Command and drives the uniform pipeline end to
// end: invoke Run, map its (*Result | error) to an Envelope via the assemble
// constructors, serialize that envelope in the chosen Format, and return the process
// exit code (ExitFor). It is the SOLE assembler + serializer + exit-deriver, so every
// command — whatever its outcome — emits exactly one envelope and exits the same way.
//
// The returned error is reserved for an INFRASTRUCTURE failure (could not write the
// envelope to w); in that case the exit code is ExitInternal and the caller (cmd/wi)
// reports it on stderr. A domain failure is NOT a returned error — it is carried inside
// the emitted envelope with its mapped non-zero exit code.
func Execute(ctx context.Context, w io.Writer, m Meta, format Format, cmd Command) (contract.ExitCode, error) {
	r, err := cmd.Run(ctx)
	env := envelopeFor(m, r, err)
	if werr := Render(w, env, format); werr != nil {
		return contract.ExitInternal, werr
	}
	return ExitFor(env), nil
}

// envelopeFor maps a Command's typed outcome onto an assembled Envelope, enforcing the
// return convention documented on Command. A *CommandError selects the kind/action and
// hints; any other error becomes kind=internal; a nil error with a Result is a success
// with every additive block threaded on. A partial (Result + *CommandError) builds the
// failure envelope from the error, then threads the result's per-repo detail onto it so
// "what completed" survives alongside the top-level error.kind=partial (decision #D).
func envelopeFor(m Meta, r *Result, err error) contract.Envelope {
	switch {
	case err != nil:
		var env contract.Envelope
		var ce *CommandError
		if errors.As(err, &ce) {
			action := ce.Action
			if action == "" {
				action = contract.ActionNoop
			}
			env = Failure(m, action, contract.Error{
				Kind:       ce.Kind,
				Message:    ce.Message,
				Repo:       ce.Repo,
				Help:       ce.Help,
				DidYouMean: ce.DidYouMean,
			})
		} else {
			env = Failure(m, contract.ActionNoop, contract.Error{
				Kind:    contract.KindInternal,
				Message: err.Error(),
			})
		}
		// Durable partial: carry what completed alongside the top-level error.
		if r != nil {
			env.Repos = r.Repos
			env.Warnings = r.Warnings
			env.Next = r.Next
		}
		return env

	case r != nil:
		env := Success(m, r.Action, r.Repos)
		env.Resolve = r.Resolve
		env.Planned = r.Planned
		env.Blocked = r.Blocked
		env.Warnings = r.Warnings
		env.Next = r.Next
		return env

	default:
		// Contract violation: a Command must return a result or an error.
		return Failure(m, contract.ActionNoop, contract.Error{
			Kind:    contract.KindInternal,
			Message: "command returned neither a result nor an error",
		})
	}
}
