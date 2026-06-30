package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// starterManifest is the scaffold wi.config.jsonc that `wi init` writes into a fresh
// workspace: an empty defaults block and an empty repos list, with the shape a user
// fills in shown as JSONC comments. It MUST parse under config.Parse (an empty
// manifest is valid) — the CMD-INIT guard dogfoods exactly that by loading the
// written file back through config.Load, so the emitter and the reader cannot drift.
const starterManifest = `// wi.config.jsonc — workspace-isolation manifest.
// Declare the repositories wi manages as detached-HEAD SSOT clones under repos/.
{
  "defaults": {
    // "base": ["dev", "main"]   // base a repo inherits: first existing branch wins ("main" is also fine)
  },
  "repos": [
    // { "name": "api", "url": "git@github.com:you/api.git", "base": ["dev", "main"] }
  ]
}
`

// newInitCommand is the `wi init` factory. init defines a workspace at the resolved
// root (decision #G — root = cwd, the layout cmd/wi resolves at startup), so it takes
// NO positional operand; a surplus arg is a usage refusal (an explicit --root/-C
// override and parent walk-up are deferred, both additive and contract-neutral).
func newInitCommand(l layout.Layout, args []string) (Command, error) {
	if len(args) != 0 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi init"}
	}
	return &initCmd{layout: l}, nil
}

// initCmd scaffolds a new workspace at its bound root: it Bootstraps the .wi/ runtime
// subtree (idempotent), then writes the starter manifest LAST as the commit point —
// an O_EXCL create, so the manifest's presence reliably marks a completed init and a
// re-init refuses cleanly with already_exists rather than clobbering a real manifest.
// Bootstrap precedes the manifest write so a Bootstrap failure leaves no manifest and
// a retry starts clean. No git, no network.
type initCmd struct {
	layout layout.Layout
}

func (c *initCmd) Run(ctx context.Context) (*Result, error) {
	if err := c.layout.Bootstrap(); err != nil {
		return nil, fmt.Errorf("init: bootstrap workspace: %w", err)
	}

	configPath := c.layout.Config()
	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, &CommandError{
				Kind:    contract.KindAlreadyExists,
				Message: fmt.Sprintf("already a wi workspace: %s exists", configPath),
				Help:    "add a repo with: wi repo add <name> <url>",
			}
		}
		return nil, fmt.Errorf("init: create manifest: %w", err)
	}
	_, writeErr := f.WriteString(starterManifest)
	closeErr := f.Close()
	if writeErr != nil {
		return nil, fmt.Errorf("init: write manifest: %w", writeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("init: finalize manifest: %w", closeErr)
	}

	return &Result{
		Action: contract.ActionCreated,
		Next:   []string{"declare repos in wi.config.jsonc, then: wi isolate new <task> <repo>…"},
	}, nil
}
