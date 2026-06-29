package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// Guard RUN-PIPELINE: cli.Execute is the SOLE place a command's typed outcome becomes
// an envelope + a process exit code. It runs one already-constructed Command, maps the
// (*Result | error) it returns through the assemble constructors (Success/Failure),
// serializes via the chosen Format (json=Emit, text=RenderText), and returns ExitFor.
// Handlers therefore never assemble an envelope or pick an exit code — this is the one
// wiring point, so every command exits the same way regardless of outcome.
//
// The cases below drive every outcome class through the real pipeline with a fake
// Command: success (all blocks threaded, exit 0), a typed CommandError (kind+hints
// preserved, exit from the §3.2 matrix), a plain error (kind=internal, exit 70), a
// durable partial (Result + CommandError{kind:partial} → ok:false but repos[] carried,
// exit 2 — decision #D), text-format dispatch, and a dry-run whose blocked[] verdict is
// exit-neutral (exit 0).
//
// Non-vacuity mutant (registered): drop the partial result-merge in envelopeFor
// (`if r != nil { env.Repos = r.Repos … }`) → the partial no longer carries per-repo
// detail → TestExecutePartialCarriesReposAndExitsTwo RED; or make Execute ignore ExitFor
// and `return contract.ExitOK` → every non-zero-exit assertion RED.

// fakeCommand is a Command whose Run returns canned (result, err), so the guard can
// drive each outcome class through the real Runner without any domain work.
type fakeCommand struct {
	result *cli.Result
	err    error
}

func (c fakeCommand) Run(ctx context.Context) (*cli.Result, error) { return c.result, c.err }

func decodeOne(t *testing.T, r io.Reader) contract.Envelope {
	t.Helper()
	var env contract.Envelope
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

func TestExecuteSuccessThreadsResultAndExitsZero(t *testing.T) {
	var buf bytes.Buffer
	m := cli.Meta{OpID: "op_x", Command: "isolate new"}
	res := &cli.Result{
		Action: contract.ActionCreated,
		Repos:  []contract.RepoResult{{Repo: "api", Action: contract.ActionCreated}},
		Next:   []string{"wi resolve t1"},
	}

	code, err := cli.Execute(context.Background(), &buf, m, cli.FormatJSON, fakeCommand{result: res})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if code != contract.ExitOK {
		t.Errorf("exit = %d, want %d", code, contract.ExitOK)
	}

	env := decodeOne(t, &buf)
	if !env.OK {
		t.Error("success envelope must have ok=true")
	}
	if env.Error != nil {
		t.Errorf("success envelope must have nil error, got %+v", env.Error)
	}
	if env.Action != contract.ActionCreated {
		t.Errorf("Action = %q, want %q", env.Action, contract.ActionCreated)
	}
	if env.OpID != "op_x" || env.Command != "isolate new" {
		t.Errorf("meta not threaded: op_id=%q command=%q", env.OpID, env.Command)
	}
	if len(env.Repos) != 1 || env.Repos[0].Repo != "api" {
		t.Errorf("repos not threaded: %+v", env.Repos)
	}
	if len(env.Next) != 1 || env.Next[0] != "wi resolve t1" {
		t.Errorf("next not threaded: %+v", env.Next)
	}
}

func TestExecuteCommandErrorMapsKindAndExit(t *testing.T) {
	var buf bytes.Buffer
	m := cli.Meta{OpID: "op_y", Command: "repo add"}
	cmdErr := &cli.CommandError{
		Kind:    contract.KindNotFound,
		Message: "no such repo",
		Repo:    "ghost",
		Help:    "run wi init",
	}

	code, err := cli.Execute(context.Background(), &buf, m, cli.FormatJSON, fakeCommand{err: cmdErr})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if code != contract.ExitNotFound {
		t.Errorf("exit = %d, want %d", code, contract.ExitNotFound)
	}

	env := decodeOne(t, &buf)
	if env.OK {
		t.Error("failure envelope must have ok=false")
	}
	if env.Error == nil {
		t.Fatal("failure envelope must carry a non-nil error")
	}
	if env.Error.Kind != contract.KindNotFound {
		t.Errorf("Error.Kind = %q, want %q", env.Error.Kind, contract.KindNotFound)
	}
	if env.Error.Repo != "ghost" || env.Error.Help != "run wi init" {
		t.Errorf("CommandError hints not preserved: %+v", env.Error)
	}
	if env.Action != contract.ActionNoop {
		t.Errorf("Action = %q, want %q (a refused command acted on nothing)", env.Action, contract.ActionNoop)
	}
}

func TestExecutePlainErrorMapsToInternal(t *testing.T) {
	var buf bytes.Buffer
	m := cli.Meta{OpID: "op_z", Command: "sync"}

	code, err := cli.Execute(context.Background(), &buf, m, cli.FormatJSON, fakeCommand{err: errors.New("boom")})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if code != contract.ExitInternal {
		t.Errorf("exit = %d, want %d", code, contract.ExitInternal)
	}

	env := decodeOne(t, &buf)
	if env.Error == nil || env.Error.Kind != contract.KindInternal {
		t.Fatalf("an unclassified error must map to kind=internal, got %+v", env.Error)
	}
	if !strings.Contains(env.Error.Message, "boom") {
		t.Errorf("internal error must surface the underlying message, got %q", env.Error.Message)
	}
}

func TestExecutePartialCarriesReposAndExitsTwo(t *testing.T) {
	var buf bytes.Buffer
	m := cli.Meta{OpID: "op_p", Command: "isolate new"}
	res := &cli.Result{
		Action: contract.ActionCreated,
		Repos: []contract.RepoResult{
			{Repo: "api", Action: contract.ActionCreated},
			{Repo: "web", Action: contract.ActionNoop, Error: &contract.Error{Kind: contract.KindDirtyWorktree, Message: "dirty"}},
		},
	}
	// A durable partial: the command both produced per-repo detail AND failed overall.
	perr := &cli.CommandError{Kind: contract.KindPartial, Action: contract.ActionCreated, Message: "1 of 2 repos created"}

	code, err := cli.Execute(context.Background(), &buf, m, cli.FormatJSON, fakeCommand{result: res, err: perr})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if code != contract.ExitPartial {
		t.Errorf("exit = %d, want %d", code, contract.ExitPartial)
	}

	env := decodeOne(t, &buf)
	if env.OK {
		t.Error("a partial must be ok=false")
	}
	if env.Error == nil || env.Error.Kind != contract.KindPartial {
		t.Fatalf("partial must carry top-level error.kind=partial, got %+v", env.Error)
	}
	if env.Action != contract.ActionCreated {
		t.Errorf("Action = %q, want %q (the in-flight verb rides on a partial)", env.Action, contract.ActionCreated)
	}
	if len(env.Repos) != 2 {
		t.Errorf("a partial must carry per-repo detail alongside the top-level error, got %d repos", len(env.Repos))
	}
}

func TestExecuteRendersTextWhenFormatText(t *testing.T) {
	var buf bytes.Buffer
	m := cli.Meta{OpID: "op_t", Command: "resolve"}

	code, err := cli.Execute(context.Background(), &buf, m, cli.FormatText, fakeCommand{err: &cli.CommandError{Kind: contract.KindNotFound, Message: "nope"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if code != contract.ExitNotFound {
		t.Errorf("exit = %d, want %d", code, contract.ExitNotFound)
	}

	out := buf.String()
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("FormatText must not emit JSON, got:\n%s", out)
	}
	if !strings.Contains(out, "FAILED") || !strings.Contains(out, "not_found") {
		t.Errorf("text render must show the human status + error kind, got:\n%s", out)
	}
}

func TestExecuteBlockedDryRunExitsZero(t *testing.T) {
	var buf bytes.Buffer
	m := cli.Meta{OpID: "op_d", Command: "isolate new", DryRun: true}
	res := &cli.Result{
		Action:  contract.ActionNoop,
		Blocked: []contract.BlockItem{{Repo: "api", Kind: contract.KindDirtyWorktree, Reason: "would refuse: dirty worktree"}},
	}

	code, err := cli.Execute(context.Background(), &buf, m, cli.FormatJSON, fakeCommand{result: res})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if code != contract.ExitOK {
		t.Errorf("a dry-run with a blocked verdict must exit 0 (blocked is exit-neutral), got %d", code)
	}

	env := decodeOne(t, &buf)
	if !env.DryRun {
		t.Error("dry-run flag must thread through to the envelope")
	}
	if len(env.Blocked) != 1 {
		t.Errorf("blocked verdicts must thread through, got %d", len(env.Blocked))
	}
}
