package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/help"
	"github.com/ggkguelensan/workspace-isolation/internal/suggest"
)

// newHelpCommand is the `wi help [topic]` factory. It takes ANY number of positional
// args (none → the overview; one or more → a topic), so there is no usage error to
// raise here: an unknown topic is a domain not_found surfaced in Run, not a malformed
// invocation. Multi-token topics ("isolate new") arrive as separate args because
// Dispatch resolves the 1-token "help" and hands the rest through — the handler rejoins
// them into the canonical command string help.For expects.
func newHelpCommand(args []string) (Command, error) {
	return &helpCmd{topic: strings.Join(args, " ")}, nil
}

// helpCmd answers `wi help [topic]` by a PURE projection of internal/help onto the
// contract: help.For(topic) yields the progressive-disclosure Model, which maps to the
// envelope's additive help block (descriptive content) plus next[] (the runnable
// follow-ups). It is the command that backs the advertised help-json capability. No
// I/O, no git, no network. An unknown topic returns the zero Model + ok=false, which
// becomes a not_found refusal carrying a did_you_mean hint from internal/suggest rather
// than inventing help text.
type helpCmd struct {
	topic string
}

func (c *helpCmd) Run(ctx context.Context) (*Result, error) {
	model, ok := help.For(c.topic)
	if !ok {
		return nil, &CommandError{
			Kind:       contract.KindNotFound,
			Message:    fmt.Sprintf("unknown help topic: %q", c.topic),
			Help:       "wi help",
			DidYouMean: suggest.For(c.topic, helpCommandNames()),
		}
	}
	return &Result{
		Action: contract.ActionRead,
		Help:   helpBlockFromModel(model),
		Next:   model.Next,
	}, nil
}

// helpBlockFromModel maps the pure help.Model onto the contract's wire types. The cli
// layer owns this translation so internal/contract never imports internal/help and vice
// versa (DESIGN §3.1 — contract is the sole owner of the wire type; help is pure). The
// overview's command table maps to commands[]; a single-command Model leaves Commands
// nil, so the block carries only that command's topic/synopsis/usage.
func helpBlockFromModel(m help.Model) *contract.HelpBlock {
	block := &contract.HelpBlock{
		Topic:    m.Topic,
		Synopsis: m.Synopsis,
		Usage:    m.Usage,
	}
	for _, cmd := range m.Commands {
		block.Commands = append(block.Commands, contract.HelpCommand{
			Name:     cmd.Name,
			Synopsis: cmd.Synopsis,
			Usage:    cmd.Usage,
		})
	}
	return block
}

// helpCommandNames is the candidate set suggest.For ranks an unknown topic against: the
// canonical command names from the help table (the same surface the overview lists).
func helpCommandNames() []string {
	cmds := help.Commands()
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name
	}
	return names
}
