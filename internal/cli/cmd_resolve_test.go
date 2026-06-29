package cli_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Guard CMD-RESOLVE: the `wi resolve <task>` handler is the first per-command unit —
// a Command (built by the BuildRegistry factory over a bound layout) that answers
// "where is everything for this task?" by a PURE projection: load the durable state
// record, project it through resolve.Bundle, return Result{Action: read, Resolve: …}.
// It NEVER assembles an envelope or picks an exit code (the pipeline owns that). The
// domain mappings this guard pins, which the generic RUN-PIPELINE/DISPATCH-ROUTES
// guards do NOT cover: a present record → a read Result whose resolve block carries the
// independently-derived paths; a MISSING record (state.ErrNoRecord) → a *CommandError
// with kind=not_found (NOT the plain-error → internal path); and the factory's arg
// validation (exactly one task arg, a safe segment) → kind=usage.
//
// Non-vacuity mutant (registered): in resolveCommand.Run drop the
// `errors.Is(err, state.ErrNoRecord)` branch (let a missing record fall through as a
// plain error) → a missing isolate maps to kind=internal, not not_found →
// TestResolveMissingIsolateIsNotFound RED (wrong kind); or make the factory accept any
// arg count (skip the len(args)!=1 check) → TestResolveFactoryValidatesArgs RED.

// bootstrappedLayout returns a layout over a fresh temp root with the .wi/ subtree
// materialized, so state.Store has a real state dir to write into.
func bootstrappedLayout(t *testing.T) layout.Layout {
	t.Helper()
	l, err := layout.Resolve(t.TempDir())
	if err != nil {
		t.Fatalf("resolve layout: %v", err)
	}
	if err := l.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return l
}

func resolveFactory(t *testing.T, l layout.Layout) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l})["resolve"]
	if !ok {
		t.Fatal("BuildRegistry has no \"resolve\" factory")
	}
	return f
}

func TestResolveProjectsLoadedRecord(t *testing.T) {
	l := bootstrappedLayout(t)
	rec := state.NewIsolateRecord("t1", "op_x", []string{"api", "web"})
	if err := state.Store(l.StateDir(), rec); err != nil {
		t.Fatalf("store record: %v", err)
	}

	cmd, err := resolveFactory(t, l)([]string{"t1"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Action != contract.ActionRead {
		t.Errorf("Action = %q, want %q", res.Action, contract.ActionRead)
	}
	if res.Resolve == nil {
		t.Fatal("resolve block must be populated")
	}

	// Independent derivation of the expected paths (join the scheme literals over the
	// normalized root, NOT via the layout accessors the implementation uses).
	root := l.Root()
	if want := filepath.Join(root, "isolas", "t1"); res.Resolve.IsolateRoot != want {
		t.Errorf("isolate_root = %q, want %q", res.Resolve.IsolateRoot, want)
	}
	if want := filepath.Join(root, ".wi", "state"); res.Resolve.StateDir != want {
		t.Errorf("state_dir = %q, want %q", res.Resolve.StateDir, want)
	}
	if len(res.Resolve.Repos) != 2 {
		t.Fatalf("want 2 repos projected, got %d", len(res.Resolve.Repos))
	}
	api := res.Resolve.Repos[0]
	if want := filepath.Join(root, "isolas", "t1", "api"); api.Worktree != want {
		t.Errorf("api worktree = %q, want %q", api.Worktree, want)
	}
	if want := filepath.Join(root, "repos", "api"); api.Mirror != want {
		t.Errorf("api mirror = %q, want %q", api.Mirror, want)
	}
}

func TestResolveMissingIsolateIsNotFound(t *testing.T) {
	l := bootstrappedLayout(t)

	cmd, err := resolveFactory(t, l)([]string{"ghost"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(context.Background())
	if res != nil {
		t.Errorf("a missing isolate must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindNotFound {
		t.Errorf("Kind = %q, want %q (a missing isolate is not_found, not internal)", ce.Kind, contract.KindNotFound)
	}
	if !contains(ce.Message, "ghost") {
		t.Errorf("not_found message should name the task, got %q", ce.Message)
	}
}

func TestResolveFactoryValidatesArgs(t *testing.T) {
	l := bootstrappedLayout(t)
	f := resolveFactory(t, l)

	for _, args := range [][]string{nil, {}, {"a", "b"}} {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("args %v: want kind=usage, got %v", args, err)
		}
	}
	// a traversing task name is rejected at the factory (usage), never reaching state.
	if _, err := f([]string{"../evil"}); !isUsage(err) {
		t.Errorf("traversing task name: want kind=usage, got %v", err)
	}
	// exactly one safe arg → a runnable Command.
	cmd, err := f([]string{"t1"})
	if err != nil || cmd == nil {
		t.Errorf("one safe task arg must build a Command, got cmd=%v err=%v", cmd, err)
	}
}

func isUsage(err error) bool {
	var ce *cli.CommandError
	return errors.As(err, &ce) && ce.Kind == contract.KindUsage
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
