package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Guard CMD-STATE-CAS (fitness, level: unit) — DESIGN §8.
//
// `wi state cas <ns> <key> --expected <v|__ABSENT__> --new <v>` is the seam between
// the namespaced compare-and-swap core (state.KVCompareAndSwap) and the envelope
// contract — the FIRST command to back the state-kv capability, the agent-coordination
// primitive. Its load-bearing mapping (decision #SKV-3):
//   - a won CAS         → *Result{action=created}, nil  (ok envelope, exit 0);
//   - a lost CAS (the precondition did not hold) → *CommandError{conflict, noop}
//     (exit 4): a TYPED refusal distinguishable from success by exit code alone, NOT
//     an infra error — the agent re-reads and retries;
//   - a held namespace  → *CommandError{lock_held} (exit 6);
//   - bad operands/flags → usage (exit 64) refused at the factory, before any I/O;
//   - --dry-run         → *Result{action=noop}, nil and NO write (exit 0), mirroring
//     land (v1 does not model a CAS preview — it would race the real value anyway).
//
// Non-vacuity mutant (registered, primary): in stateCasCmd.Run map the lost CAS
// (!swapped) to a created success instead of *CommandError{conflict} → a lost race is
// mis-reported as a win → TestStateCasCompareMiss RED. Alternate mutant: drop the
// DryRunFrom(ctx) guard so --dry-run executes the swap → TestStateCasDryRunNoWrite RED
// (the value it must NOT have written is present).

func stateCasFactory(t *testing.T, l layout.Layout) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l})["state cas"]
	if !ok {
		t.Fatal("BuildRegistry has no \"state cas\" factory")
	}
	return f
}

// runStateCas builds and runs `state cas` over a bootstrapped layout, threading a
// fixed op_id so a successful swap stamps a deterministic holder.
func runStateCas(t *testing.T, l layout.Layout, dryRun bool, args ...string) (*cli.Result, error) {
	t.Helper()
	cmd, err := stateCasFactory(t, l)(args)
	if err != nil {
		return nil, err
	}
	ctx := cli.WithOpID(context.Background(), "op_state_cas")
	if dryRun {
		ctx = cli.WithDryRun(ctx, true)
	}
	return cmd.Run(ctx)
}

func TestStateCasSwapFromAbsent(t *testing.T) {
	l := bootstrappedLayout(t)

	// Claim a slot from absent via the frozen sentinel: a won CAS is action=created.
	res, err := runStateCas(t, l, false, "ports", "alpha", "--expected", state.AbsentSentinel, "--new", "3000")
	if err != nil {
		t.Fatalf("CAS from absent: unexpected error %v", err)
	}
	if res == nil || res.Action != contract.ActionCreated {
		t.Fatalf("CAS from absent: result=%+v, want action=created", res)
	}
	// It must be durable: a fresh read sees the value the command wrote.
	if v, ok, err := state.KVGet(l, "ports", "alpha"); err != nil || !ok || v != "3000" {
		t.Fatalf("after won CAS: KVGet=%q ok=%v err=%v, want \"3000\" true nil", v, ok, err)
	}

	// A subsequent value-matched swap also wins (--expected=value form).
	res, err = runStateCas(t, l, false, "ports", "alpha", "--expected=3000", "--new=4000")
	if err != nil {
		t.Fatalf("matched swap: unexpected error %v", err)
	}
	if res == nil || res.Action != contract.ActionCreated {
		t.Fatalf("matched swap: result=%+v, want action=created", res)
	}
	if v, _, _ := state.KVGet(l, "ports", "alpha"); v != "4000" {
		t.Fatalf("after matched swap: KVGet=%q, want 4000", v)
	}
}

func TestStateCasCompareMiss(t *testing.T) {
	l := bootstrappedLayout(t)
	if _, err := runStateCas(t, l, false, "ports", "alpha", "--expected", state.AbsentSentinel, "--new", "3000"); err != nil {
		t.Fatalf("seed CAS: %v", err)
	}

	// The precondition no longer holds (alpha is 3000, not 9999): a LOST CAS is a typed
	// conflict refusal with action=noop — never a created success, never an infra error.
	_, err := runStateCas(t, l, false, "ports", "alpha", "--expected", "9999", "--new", "5000")
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("compare miss: err=%v, want *cli.CommandError", err)
	}
	if ce.Kind != contract.KindConflict {
		t.Fatalf("compare miss: kind=%q, want %q", ce.Kind, contract.KindConflict)
	}
	if ce.Action != contract.ActionNoop {
		t.Fatalf("compare miss: action=%q, want %q", ce.Action, contract.ActionNoop)
	}
	// A lost CAS writes nothing.
	if v, _, _ := state.KVGet(l, "ports", "alpha"); v != "3000" {
		t.Fatalf("lost CAS wrote anyway: KVGet=%q, want unchanged 3000", v)
	}
}

func TestStateCasLockHeld(t *testing.T) {
	l := bootstrappedLayout(t)
	key, err := lock.StateKV("ports")
	if err != nil {
		t.Fatal(err)
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		t.Fatalf("pre-acquire ports lock: %v", err)
	}
	defer func() { _ = held.Release() }()

	// The namespace lock is already held → the CAS surfaces lock_held (exit 6), the
	// uniform contended-lock posture, not a torn write.
	_, err = runStateCas(t, l, false, "ports", "alpha", "--expected", state.AbsentSentinel, "--new", "3000")
	var ce *cli.CommandError
	if !errors.As(err, &ce) || ce.Kind != contract.KindLockHeld {
		t.Fatalf("CAS on held namespace: err=%v, want *cli.CommandError{lock_held}", err)
	}
}

func TestStateCasDryRunNoWrite(t *testing.T) {
	l := bootstrappedLayout(t)

	res, err := runStateCas(t, l, true, "ports", "alpha", "--expected", state.AbsentSentinel, "--new", "3000")
	if err != nil {
		t.Fatalf("dry-run CAS: unexpected error %v", err)
	}
	if res == nil || res.Action != contract.ActionNoop {
		t.Fatalf("dry-run CAS: result=%+v, want action=noop", res)
	}
	// A --dry-run must perform NO write: the key stays absent.
	if v, ok, _ := state.KVGet(l, "ports", "alpha"); ok {
		t.Fatalf("dry-run wrote anyway: KVGet=%q ok=true, want absent", v)
	}
}

func TestStateCasFactoryValidation(t *testing.T) {
	l := bootstrappedLayout(t)
	f := stateCasFactory(t, l)
	cases := map[string][]string{
		"too few positionals":  {"ports"},
		"missing --new":        {"ports", "alpha", "--expected", "__ABSENT__"},
		"missing --expected":   {"ports", "alpha", "--new", "3000"},
		"extra positional":     {"ports", "alpha", "extra", "--expected", "x", "--new", "y"},
		"--expected w/o value": {"ports", "alpha", "--new", "3000", "--expected"},
		"traversal namespace":  {"../etc", "alpha", "--expected", "__ABSENT__", "--new", "3000"},
		"empty key":            {"ports", "", "--expected", "__ABSENT__", "--new", "3000"},
		"unknown flag":         {"ports", "alpha", "--bogus", "z", "--expected", "x", "--new", "y"},
	}
	for name, args := range cases {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("%s: err=%v, want a usage CommandError", name, err)
		}
	}
}
