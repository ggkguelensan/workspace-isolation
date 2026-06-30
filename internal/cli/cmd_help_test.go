package cli_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/help"
)

// Guard CMD-HELP: the `wi help [topic]` handler is the command that finally BACKS the
// advertised help-json capability (closing the PLAN line 108 "capabilities ⇒ backing
// command" gap). It is a pure projection of internal/help onto the contract:
//   - no topic            → the overview Result (Help block lists every command, runnable
//                           follow-ups on next[]).
//   - a command topic     → that command's detail (Help block topic/synopsis/usage, the
//                           command table NOT re-listed). Multi-token topics ("isolate
//                           new") are joined from the dispatched args.
//   - an unknown topic    → a *CommandError{not_found} carrying a did_you_mean hint from
//                           internal/suggest + the `wi help` pointer (NOT the plain-error
//                           → internal path).
// And — end to end through Execute — the Help block actually reaches the emitted envelope
// (the help-json capability has a real wire payload).
//
// Non-vacuity mutants (registered): in helpCmd.Run drop the `!ok` not_found branch (map
// the zero Model to a Result instead) → TestHelpUnknownTopicIsNotFound RED (got a result,
// want *CommandError); or in envelopeFor drop the `env.Help = r.Help` threading → the
// help block never reaches the wire → TestHelpEnvelopeCarriesBlockEndToEnd RED (env.Help
// nil).

func helpFactory(t *testing.T) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{})["help"]
	if !ok {
		t.Fatal(`BuildRegistry has no "help" factory`)
	}
	return f
}

func TestHelpOverviewListsEveryCommand(t *testing.T) {
	cmd, err := helpFactory(t)(nil)
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
	if res.Help == nil {
		t.Fatal("overview must populate the help block")
	}
	if res.Help.Topic != "" {
		t.Errorf("overview Topic = %q, want empty", res.Help.Topic)
	}

	// The block's command surface must mirror help.Commands() exactly (the single source
	// of truth) — name + synopsis + usage, in order.
	want := help.Commands()
	if len(res.Help.Commands) != len(want) {
		t.Fatalf("overview lists %d commands, want %d", len(res.Help.Commands), len(want))
	}
	for i, c := range want {
		got := res.Help.Commands[i]
		if got.Name != c.Name || got.Synopsis != c.Synopsis || got.Usage != c.Usage {
			t.Errorf("command %d = %+v, want name=%q synopsis=%q usage=%q", i, got, c.Name, c.Synopsis, c.Usage)
		}
	}
	// Runnable follow-ups ride next[], not the descriptive block.
	if len(res.Next) == 0 {
		t.Error("overview must carry runnable next[] follow-ups")
	}
}

func TestHelpCommandTopicDetail(t *testing.T) {
	// `wi help isolate new` dispatches "help" with args ["isolate","new"]; the handler
	// joins them into the multi-token topic.
	cmd, err := helpFactory(t)([]string{"isolate", "new"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Help == nil {
		t.Fatal("a command topic must populate the help block")
	}
	if res.Help.Topic != "isolate new" {
		t.Errorf("Topic = %q, want %q (multi-token topic joined from args)", res.Help.Topic, "isolate new")
	}
	if res.Help.Synopsis == "" || res.Help.Usage == "" {
		t.Errorf("command detail must carry synopsis+usage, got %+v", res.Help)
	}
	// Drilling into a command does NOT re-list the whole table.
	if res.Help.Commands != nil {
		t.Errorf("command detail must not re-list the table, got %d commands", len(res.Help.Commands))
	}
}

func TestHelpUnknownTopicIsNotFound(t *testing.T) {
	// "snc" is one edit from the registered "sync" → not_found with a did_you_mean hint.
	cmd, err := helpFactory(t)([]string{"snc"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(context.Background())
	if res != nil {
		t.Errorf("an unknown topic must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindNotFound {
		t.Errorf("Kind = %q, want %q (an unknown topic is not_found, not internal)", ce.Kind, contract.KindNotFound)
	}
	if !slices.Contains(ce.DidYouMean, "sync") {
		t.Errorf("did_you_mean = %v, want it to include %q", ce.DidYouMean, "sync")
	}
	if ce.Help != "wi help" {
		t.Errorf("Help = %q, want %q", ce.Help, "wi help")
	}
}

// TestHelpEnvelopeCarriesBlockEndToEnd drives the real help command through cli.Execute
// and asserts the Help block survives onto the emitted envelope — the help-json
// capability must have a wire payload, not just a Result field.
func TestHelpEnvelopeCarriesBlockEndToEnd(t *testing.T) {
	cmd, err := helpFactory(t)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	var buf bytes.Buffer
	m := cli.Meta{OpID: "op_help", Command: "help"}
	code, err := cli.Execute(context.Background(), &buf, m, cli.FormatJSON, cmd)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if code != contract.ExitOK {
		t.Errorf("exit = %d, want %d", code, contract.ExitOK)
	}

	env := decodeOne(t, &buf)
	if !env.OK || env.Action != contract.ActionRead {
		t.Errorf("help envelope must be ok=true action=read, got ok=%v action=%q", env.OK, env.Action)
	}
	if env.Help == nil {
		t.Fatal("the help block must reach the emitted envelope (help-json capability payload)")
	}
	if env.Help.Synopsis == "" || len(env.Help.Commands) == 0 {
		t.Errorf("emitted help block is empty: %+v", env.Help)
	}
}
