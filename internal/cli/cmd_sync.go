package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/config"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	syncpkg "github.com/ggkguelensan/workspace-isolation/internal/sync"
)

// newSyncCommand is the `wi sync [<repo>…]` factory: it binds the (possibly empty) repo
// operands plus the layout, git driver, and clock into a runnable Command. There is no
// positional-arg validation here — `wi sync` with no operands is valid (it syncs every
// declared repo), and each named repo is checked against the manifest in Run (an
// undeclared one is not_found, not usage: the operand is well-formed, it just names
// nothing wi manages). This mirrors isolate new, which likewise defers repo-name checks
// to Run.
func newSyncCommand(l layout.Layout, g *git.Git, clk clock.Clock, args []string) (Command, error) {
	return &syncCmd{layout: l, git: g, clock: clk, repos: args}, nil
}

// syncCmd is the seam between the sync domain core (continue-on-fail, decision #S) and the
// envelope contract. Run (a) loads the manifest and resolves which repos to sync — no
// operands means EVERY declared repo, named operands a subset; a missing manifest is
// not_found (+`wi init`), an undeclared repo is not_found, a malformed manifest is usage
// (the same two-way split isolate new uses, decision #H); (b) reads the minted op_id from
// the context (CTX-OPID) so the run's identity matches the envelope; (c) drives sync.Run
// and maps its Status onto the return convention — complete → a synced Result, partial →
// the durable (result, *CommandError{Kind: partial}) carrying per-repo detail (decision
// #D). It never assembles an envelope or chooses an exit code (the pipeline owns that).
type syncCmd struct {
	layout layout.Layout
	git    *git.Git
	clock  clock.Clock
	repos  []string
}

func (c *syncCmd) Run(ctx context.Context) (*Result, error) {
	cfg, err := config.Load(c.layout.Config())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &CommandError{
				Kind:    contract.KindNotFound,
				Message: fmt.Sprintf("no wi workspace here: %s not found", c.layout.Config()),
				Help:    "create one with: wi init",
			}
		}
		// A malformed manifest is user-fixable input, not a wi bug → usage (exit 64).
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}

	selected, err := c.selectRepos(cfg)
	if err != nil {
		return nil, err
	}

	specs := make([]syncpkg.RepoSpec, len(selected))
	for i, r := range selected {
		specs[i] = syncpkg.RepoSpec{Name: r.Name, URL: r.URL, Base: r.Base}
	}

	res, err := syncpkg.Run(ctx, c.layout, c.git, c.clock, OpIDFrom(ctx), specs)
	if err != nil {
		// sync.Run reserves its error for an op-level failure; v0 folds every failure
		// into a per-repo outcome, so reaching here is an unclassified internal fault.
		return nil, fmt.Errorf("sync: %w", err)
	}

	repos := make([]contract.RepoResult, len(res.Repos))
	for i, oc := range res.Repos {
		repos[i] = c.projectSyncOutcome(oc)
	}

	if res.Status == syncpkg.StatusPartial {
		// Durable partial: a result (what synced) AND a top-level partial error.
		return &Result{Action: contract.ActionSynced, Repos: repos},
			&CommandError{
				Kind:    contract.KindPartial,
				Action:  contract.ActionSynced,
				Message: "sync partially completed — see repos[] for per-repo status",
			}
	}

	return &Result{Action: contract.ActionSynced, Repos: repos}, nil
}

// selectRepos resolves the operands into the manifest repos to sync: no operands → every
// declared repo (in declaration order); named operands → exactly those, in the order
// given, with the FIRST undeclared name refused as not_found. Resolution happens before
// any network I/O, so a typo costs nothing.
func (c *syncCmd) selectRepos(cfg config.Config) ([]config.Repo, error) {
	if len(c.repos) == 0 {
		return cfg.Repos, nil
	}
	selected := make([]config.Repo, 0, len(c.repos))
	for _, name := range c.repos {
		r, ok := cfg.Lookup(name)
		if !ok {
			return nil, &CommandError{
				Kind:    contract.KindNotFound,
				Message: fmt.Sprintf("repo %q is not declared in the manifest", name),
				Repo:    name,
				Help:    "declare it in wi.config.jsonc, or check the name",
			}
		}
		selected = append(selected, r)
	}
	return selected, nil
}

// projectSyncOutcome maps one sync.RepoOutcome onto the wire RepoResult: a successful sync
// → action synced with the advanced base sha, base branch, SSOT mirror path, and the
// persisted freshness (behind 0 / not stale by construction); a failed repo → action noop
// plus a per-repo error. As with isolate new's projection, refining a per-repo Error.Kind
// beyond internal (a non-fast-forward is really conflict, a held lock lock_held) awaits
// the gitexec stderr classifier; until then every per-repo failure surfaces as internal.
func (c *syncCmd) projectSyncOutcome(oc syncpkg.RepoOutcome) contract.RepoResult {
	if oc.Err != nil {
		return contract.RepoResult{
			Repo:   oc.Repo,
			Action: contract.ActionNoop,
			Error: &contract.Error{
				Kind:    contract.KindInternal,
				Message: oc.Err.Error(),
				Repo:    oc.Repo,
			},
		}
	}
	rr := contract.RepoResult{
		Repo:   oc.Repo,
		Action: contract.ActionSynced,
		Branch: oc.Base,
		SHA:    oc.Snapshot.LocalBaseSHA,
	}
	// The SSOT mirror path is derivable from the (already-validated) repo name; an error
	// here is unreachable for a repo that just synced, so leave Mirror empty rather than
	// fail the projection.
	if dir, err := c.layout.Repo(oc.Repo); err == nil {
		rr.Mirror = dir
	}
	fresh := oc.Snapshot.Freshness()
	rr.Freshness = &fresh
	return rr
}
