package cli

import (
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/exitcontract"
)

// Meta is the per-invocation context the Runner threads into every envelope: the
// correlation op_id, the canonical command string, and whether this was a --dry-run.
// A handler produces domain data; the Runner pairs it with this Meta and the
// constructors below stamp the rest.
type Meta struct {
	OpID    string
	Command string
	DryRun  bool
}

// spine builds the always-identical envelope fields — the part every command emits
// the same way regardless of outcome (DESIGN §3.1): the frozen schema_version, the
// advertised capability set, and the threaded per-invocation context. Centralizing it
// here is what lets handlers return plain structs and never touch the wire shape.
func spine(m Meta) contract.Envelope {
	return contract.Envelope{
		SchemaVersion: contract.SchemaVersion,
		Capabilities:  contract.Capabilities(),
		OpID:          m.OpID,
		Command:       m.Command,
		DryRun:        m.DryRun,
	}
}

// Success assembles a success envelope: ok=true with a nil error, the given action,
// and per-repo outcomes. It is the SOLE constructor of a successful envelope, so the
// ok ⟺ error==nil coupling cannot be gotten wrong at a call site. Additive blocks
// (Resolve, Planned, Blocked) and Warnings/Next are command-specific and set by the
// caller on the returned value — they don't bear on the coupling this enforces. A
// dry-run plan is a Success whose Meta.DryRun is true and whose verdicts the caller
// hangs in Blocked/Planned (see ExitFor for why that stays exit 0).
func Success(m Meta, action contract.Action, repos []contract.RepoResult) contract.Envelope {
	e := spine(m)
	e.OK = true
	e.Action = action
	e.Repos = repos
	e.Error = nil
	return e
}

// Failure assembles an error envelope: ok=false carrying the given error payload. It
// takes the error by value and stores a copy, so the caller can't later alias-mutate
// the envelope's error. action is usually contract.ActionNoop (the command refused
// before acting) but may be the action in flight when the failure is a durable partial
// (action=created, error.kind=partial) — the partial verdict rides in error.kind, which
// ExitFor maps to exit 2 via the §3.2 matrix.
func Failure(m Meta, action contract.Action, errPayload contract.Error) contract.Envelope {
	e := spine(m)
	e.OK = false
	e.Action = action
	err := errPayload
	e.Error = &err
	return e
}

// ExitFor derives the process exit code from an assembled envelope. It is a PURE
// function of the top-level error: no top-level error -> exit 0; an error -> its §3.2
// code via exitcontract.ExitCodeFor (the single failure-matrix authority). It does NOT
// consult Blocked or DryRun.
//
// This is precisely how "every --dry-run -> exit 0" (DESIGN §3.2) is honored without a
// special case (decision #D): a dry-run that RAN puts its would-block verdicts in
// Blocked[] and leaves Error nil, so it falls through to exit 0 here — Blocked is
// exit-neutral. A genuine top-level error on a --dry-run invocation (e.g. a usage error
// that stopped the command before any plan was produced) is NOT swallowed: it still
// surfaces its real code. A blanket `if DryRun { return ExitOK }` would wrongly mask
// that, so we deliberately key only on the error.
func ExitFor(env contract.Envelope) contract.ExitCode {
	if env.Error != nil {
		return exitcontract.ExitCodeFor(env.Error.Kind)
	}
	return contract.ExitOK
}
