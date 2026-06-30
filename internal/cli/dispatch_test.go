package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/cli/opid"
	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// Guard DISPATCH-ROUTES: cli.Dispatch is the front half of the Runner — it parses argv
// (the global --format/--dry-run flags + the positional command + its args), resolves
// the named subcommand against a Registry, mints a fresh op_id (via the clock), builds
// Meta, and hands off to Execute. Every exit path emits EXACTLY ONE envelope: a known
// command runs and exits per its outcome; an unknown command, a bad --format, or a
// factory that rejects its args all become a `usage` envelope → exit 64. Decision #F:
// the parser is hand-rolled stdlib (no cobra), consistent with the zero-dep posture.
//
// Non-vacuity mutant (registered): in resolveCommand ignore the parsed name and always
// return the first registered command → TestDispatchRoutesUnknownToUsage RED (an
// unknown name wrongly runs a real command, exit 0 not 64) AND the 2-token routing
// assertion RED; or skip op_id minting (leave Meta.OpID empty) → TestDispatchMintsOpID
// RED (opid.Valid fails on "").

func fakeClock(t *testing.T) clock.Clock {
	t.Helper()
	return clock.NewFake(time.Unix(1_700_000_000, 0).UTC(), 0xD15EA5E5)
}

// recordingRegistry builds a Registry whose factories return a canned fakeCommand and
// record the args they were handed, so the guard can assert routing without any domain
// work. lastArgs[name] holds the args the named factory last received.
func recordingRegistry(t *testing.T, lastArgs map[string][]string) cli.Registry {
	t.Helper()
	mk := func(name string, res *cli.Result, factoryErr error) func([]string) (cli.Command, error) {
		return func(args []string) (cli.Command, error) {
			lastArgs[name] = args
			if factoryErr != nil {
				return nil, factoryErr
			}
			return fakeCommand{result: res}, nil
		}
	}
	return cli.Registry{
		"init": mk("init", &cli.Result{Action: contract.ActionCreated,
			Repos: []contract.RepoResult{{Repo: "api", Action: contract.ActionCreated}}}, nil),
		"isolate new": mk("isolate new", &cli.Result{Action: contract.ActionCreated}, nil),
		"repo add": mk("repo add", nil,
			&cli.CommandError{Kind: contract.KindUsage, Message: "repo add requires <name> <url>"}),
	}
}

func TestDispatchRoutesKnownCommand(t *testing.T) {
	var buf bytes.Buffer
	last := map[string][]string{}
	reg := recordingRegistry(t, last)

	code, err := cli.Dispatch(context.Background(), &buf, fakeClock(t), reg, []string{"init"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if code != contract.ExitOK {
		t.Errorf("exit = %d, want %d", code, contract.ExitOK)
	}
	env := decodeOne(t, &buf)
	if !env.OK || env.Command != "init" {
		t.Errorf("known command must run and stamp its name: ok=%v command=%q", env.OK, env.Command)
	}
	if env.Action != contract.ActionCreated || len(env.Repos) != 1 {
		t.Errorf("result not threaded through dispatch: %+v", env)
	}
}

func TestDispatchRoutesTwoTokenCommand(t *testing.T) {
	var buf bytes.Buffer
	last := map[string][]string{}
	reg := recordingRegistry(t, last)

	// "isolate new t1" → command "isolate new", command args ["t1"].
	code, err := cli.Dispatch(context.Background(), &buf, fakeClock(t), reg, []string{"isolate", "new", "t1"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if code != contract.ExitOK {
		t.Errorf("exit = %d, want %d", code, contract.ExitOK)
	}
	env := decodeOne(t, &buf)
	if env.Command != "isolate new" {
		t.Errorf("two-token command not resolved: command=%q", env.Command)
	}
	if got := last["isolate new"]; len(got) != 1 || got[0] != "t1" {
		t.Errorf("command args not forwarded after the 2-token name: %v", got)
	}
}

func TestDispatchRoutesUnknownToUsage(t *testing.T) {
	var buf bytes.Buffer
	last := map[string][]string{}
	reg := recordingRegistry(t, last)

	code, err := cli.Dispatch(context.Background(), &buf, fakeClock(t), reg, []string{"frobnicate", "x"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if code != contract.ExitUsage {
		t.Errorf("unknown command must exit %d (usage), got %d", contract.ExitUsage, code)
	}
	env := decodeOne(t, &buf)
	if env.OK || env.Error == nil || env.Error.Kind != contract.KindUsage {
		t.Fatalf("unknown command must be a usage failure, got ok=%v err=%+v", env.OK, env.Error)
	}
	if !strings.Contains(env.Error.Message, "frobnicate") {
		t.Errorf("usage message should name the unknown command, got %q", env.Error.Message)
	}
	if len(last) != 0 {
		t.Errorf("no factory should run for an unknown command, ran: %v", last)
	}
}

// TestDispatchUnknownCommandSuggests pins the agent-recovery half of the unknown-command
// path (DESIGN §3.1 / PLAN M3): the usage envelope's error.did_you_mean[] is populated
// from suggest.For over the registered command names, and error.help points at `wi help`
// so an agent can self-correct without a human. A near-miss yields the closest command; a
// token with no near match yields no did_you_mean (omitempty disappears) but still the
// help pointer.
//
// Non-vacuity mutant (registered): in the unknown-command path drop the suggest.For call
// (leave DidYouMean nil) → the "innit"→[init] assertion RED; or blank error.help → the
// help-pointer assertions RED on both the near-miss and no-match cases.
func TestDispatchUnknownCommandSuggests(t *testing.T) {
	var buf bytes.Buffer
	last := map[string][]string{}
	reg := recordingRegistry(t, last)

	// "innit" is one edit from the registered "init".
	code, err := cli.Dispatch(context.Background(), &buf, fakeClock(t), reg, []string{"innit"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if code != contract.ExitUsage {
		t.Errorf("unknown command exit = %d, want %d", code, contract.ExitUsage)
	}
	env := decodeOne(t, &buf)
	if env.Error == nil {
		t.Fatal("unknown command must be a failure envelope")
	}
	if got := env.Error.DidYouMean; len(got) != 1 || got[0] != "init" {
		t.Errorf("did_you_mean = %#v, want [init]", got)
	}
	if env.Error.Help != "wi help" {
		t.Errorf("error.help = %q, want \"wi help\"", env.Error.Help)
	}

	// A token with no near match: no did_you_mean, but still the help pointer.
	var nbuf bytes.Buffer
	if _, err := cli.Dispatch(context.Background(), &nbuf, fakeClock(t), reg, []string{"xyzzy"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	nenv := decodeOne(t, &nbuf)
	if len(nenv.Error.DidYouMean) != 0 {
		t.Errorf("no near match must yield no did_you_mean, got %#v", nenv.Error.DidYouMean)
	}
	if nenv.Error.Help != "wi help" {
		t.Errorf("error.help = %q, want \"wi help\" even with no suggestion", nenv.Error.Help)
	}
}

func TestDispatchFactoryErrorIsUsage(t *testing.T) {
	var buf bytes.Buffer
	last := map[string][]string{}
	reg := recordingRegistry(t, last)

	// "repo add" with no args: the factory rejects → usage envelope, exit 64.
	code, err := cli.Dispatch(context.Background(), &buf, fakeClock(t), reg, []string{"repo", "add"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if code != contract.ExitUsage {
		t.Errorf("factory arg-rejection must exit %d, got %d", contract.ExitUsage, code)
	}
	env := decodeOne(t, &buf)
	if env.Error == nil || env.Error.Kind != contract.KindUsage {
		t.Fatalf("factory rejection must surface as kind=usage, got %+v", env.Error)
	}
}

func TestDispatchParsesGlobalFlags(t *testing.T) {
	last := map[string][]string{}
	reg := recordingRegistry(t, last)

	// --dry-run anywhere threads into Meta.DryRun.
	var dbuf bytes.Buffer
	if _, err := cli.Dispatch(context.Background(), &dbuf, fakeClock(t), reg, []string{"isolate", "new", "t1", "--dry-run"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if env := decodeOne(t, &dbuf); !env.DryRun {
		t.Errorf("--dry-run must thread into the envelope's dry_run")
	}
	// the flag must not leak into the command's args
	if got := last["isolate new"]; len(got) != 1 || got[0] != "t1" {
		t.Errorf("--dry-run leaked into command args: %v", got)
	}

	// --format text routes serialization to RenderText (human form, not JSON).
	var tbuf bytes.Buffer
	if _, err := cli.Dispatch(context.Background(), &tbuf, fakeClock(t), reg, []string{"--format", "text", "init"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	out := tbuf.String()
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("--format text must not emit JSON, got:\n%s", out)
	}
	if !strings.Contains(out, "init") || !strings.Contains(out, "OK") {
		t.Errorf("text render missing expected content, got:\n%s", out)
	}

	// an invalid --format value is a usage error, exit 64.
	var bbuf bytes.Buffer
	code, err := cli.Dispatch(context.Background(), &bbuf, fakeClock(t), reg, []string{"--format", "yaml", "init"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if code != contract.ExitUsage {
		t.Errorf("invalid --format must exit %d, got %d", contract.ExitUsage, code)
	}
	if env := decodeOne(t, &bbuf); env.Error == nil || env.Error.Kind != contract.KindUsage {
		t.Fatalf("invalid --format must be kind=usage, got %+v", env.Error)
	}
}

func TestDispatchMintsOpID(t *testing.T) {
	var buf bytes.Buffer
	last := map[string][]string{}
	reg := recordingRegistry(t, last)

	if _, err := cli.Dispatch(context.Background(), &buf, fakeClock(t), reg, []string{"init"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	env := decodeOne(t, &buf)
	if !opid.Valid(env.OpID) {
		t.Errorf("Dispatch must mint a valid op_id, got %q", env.OpID)
	}

	// even an unknown command (usage path) gets a valid op_id for correlation.
	var ubuf bytes.Buffer
	if _, err := cli.Dispatch(context.Background(), &ubuf, fakeClock(t), reg, []string{"nope"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if env := decodeOne(t, &ubuf); !opid.Valid(env.OpID) {
		t.Errorf("usage-path envelope must still carry a valid op_id, got %q", env.OpID)
	}
}
