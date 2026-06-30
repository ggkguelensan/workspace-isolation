package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

const stateCasUsage = "usage: wi state cas <namespace> <key> --expected <value|__ABSENT__> --new <value>"

// newStateCasCommand is the `wi state cas <ns> <key> --expected <v|__ABSENT__> --new <v>`
// factory. It parses the two positionals plus the two REQUIRED value flags and binds them
// (with the layout) into a runnable Command. Both flags are mandatory on purpose: it forces
// the caller to state its precondition, and a present-but-empty value (`--new ""`) is a
// legitimate write distinct from "flag omitted". The namespace traversal check happens HERE
// so a bad name is a clean usage refusal (exit 64) before any lock or I/O — the same place
// `wi land` validates <task>. The key is a JSON map key, not a path segment, so it needs no
// traversal check, only non-emptiness. Symmetric with newLandCommand's factory.
//
// state cas is the first command carrying its own flags: parseGlobals strips only
// --dry-run/--format, so --expected/--new and their values arrive here in args verbatim.
func newStateCasCommand(l layout.Layout, args []string) (Command, error) {
	ns, key, expected, newval, err := parseStateCasArgs(args)
	if err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	if err := layout.ValidateSegment("namespace", ns); err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	return &stateCasCmd{layout: l, ns: ns, key: key, expected: expected, newval: newval}, nil
}

// parseStateCasArgs splits the post-"state cas" tokens into the namespace+key positionals
// and the two required flags. --expected/--new accept both "--flag value" and "--flag=value"
// forms; each must appear and the namespace+key must be the only two positionals. The
// __ABSENT__ sentinel (DESIGN §8) is an ordinary token here — state.AbsentSentinel
// interprets it downstream. An unknown --flag, a missing flag value, the wrong positional
// count, or an empty key is rejected so the factory can map it to a usage refusal.
func parseStateCasArgs(args []string) (ns, key, expected, newval string, err error) {
	var pos []string
	var haveExp, haveNew bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--expected", a == "--new":
			if i+1 >= len(args) {
				return "", "", "", "", fmt.Errorf("flag %s needs a value; %s", a, stateCasUsage)
			}
			i++
			if a == "--expected" {
				expected, haveExp = args[i], true
			} else {
				newval, haveNew = args[i], true
			}
		case strings.HasPrefix(a, "--expected="):
			expected, haveExp = strings.TrimPrefix(a, "--expected="), true
		case strings.HasPrefix(a, "--new="):
			newval, haveNew = strings.TrimPrefix(a, "--new="), true
		case strings.HasPrefix(a, "--"):
			return "", "", "", "", fmt.Errorf("unknown flag %q; %s", a, stateCasUsage)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return "", "", "", "", fmt.Errorf("%s", stateCasUsage)
	}
	if pos[1] == "" {
		return "", "", "", "", fmt.Errorf("key must not be empty; %s", stateCasUsage)
	}
	if !haveExp || !haveNew {
		return "", "", "", "", fmt.Errorf("both --expected and --new are required; %s", stateCasUsage)
	}
	return pos[0], pos[1], expected, newval, nil
}

// stateCasCmd is the seam between the namespaced compare-and-swap core
// (state.KVCompareAndSwap, DESIGN §8) and the envelope contract — the FIRST command to back
// the state-kv capability, the agent-coordination primitive: an agent claims a slot by
// CAS'ing from __ABSENT__, or advances shared state by CAS'ing from the value it last read.
// Run maps the core's outcome onto the return convention (decision #SKV-3, recorded
// PROGRESS.md):
//   - swapped (the precondition held) → a created Result (exit 0): the new binding is
//     committed. We reuse the existing closed Action rather than widen the enum for an
//     "updated"; on the wire ok:true + action:created marks the win;
//   - NOT swapped (the precondition did not hold) → *CommandError{conflict, noop} (exit 4):
//     the agent LOST the race / read a stale value — a TYPED refusal an agent distinguishes
//     from a win by exit code alone, NOT an infra error;
//   - a held namespace → lock_held (exit 6) via the uniform *lock.HeldError mapping;
//   - --dry-run → a noop Result (exit 0) with NO write. v1 does not model a CAS preview (it
//     would race the real value the instant it returned), mirroring land's dry-run posture.
//
// It never assembles an envelope or chooses an exit code — the pipeline owns that. The
// namespace was already traversal-checked at the factory, so a non-nil error from
// KVCompareAndSwap here is a genuine lock/IO fault → internal (exit 70), the correct default.
type stateCasCmd struct {
	layout   layout.Layout
	ns       string
	key      string
	expected string
	newval   string
}

func (c *stateCasCmd) Run(ctx context.Context) (*Result, error) {
	if DryRunFrom(ctx) {
		// A --dry-run must never write and is always exit 0. Report a safe noop carrying
		// the runnable real command; we deliberately do NOT predict would-swap (it would
		// race the live value the moment this returns).
		return &Result{
			Action: contract.ActionNoop,
			Next:   []string{fmt.Sprintf("wi state cas %s %s --expected %s --new %s", c.ns, c.key, c.expected, c.newval)},
		}, nil
	}

	swapped, err := state.KVCompareAndSwap(c.layout, c.ns, c.key, c.expected, c.newval, OpIDFrom(ctx))
	if err != nil {
		var he *lock.HeldError
		if errors.As(err, &he) {
			return nil, &CommandError{
				Kind:    contract.KindLockHeld,
				Message: fmt.Sprintf("state namespace %q is busy: %v", c.ns, err),
				Help:    "another wi state cas on this namespace is in flight; retry when it finishes",
			}
		}
		return nil, fmt.Errorf("state cas %s/%s: %w", c.ns, c.key, err)
	}

	if !swapped {
		return nil, &CommandError{
			Kind:    contract.KindConflict,
			Action:  contract.ActionNoop,
			Message: fmt.Sprintf("state cas refused: %s/%s does not match --expected", c.ns, c.key),
			Help:    "re-read the current value and retry the compare-and-swap with the value you observed",
		}
	}

	return &Result{Action: contract.ActionCreated}, nil
}
