package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/ggkguelensan/workspace-isolation/internal/baseref"
	"github.com/ggkguelensan/workspace-isolation/internal/config"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// newIsolateNewCommand is the `wi isolate new <task> <repo>…` factory: it validates the
// positional args (a safe <task> segment + at least one <repo>) and binds task/repos plus
// the layout and git driver into a runnable Command. The traversal check on <task> happens
// HERE so a bad task name is a clean usage refusal; repo names are checked against the
// manifest in Run (an undeclared one is not_found, not usage — the operand is well-formed,
// it just names nothing wi manages).
func newIsolateNewCommand(l layout.Layout, g *git.Git, args []string) (Command, error) {
	if len(args) < 2 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi isolate new <task> <repo>…"}
	}
	task := args[0]
	if err := layout.ValidateSegment("task", task); err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	return &isolateNewCmd{layout: l, git: g, task: task, repos: args[1:]}, nil
}

// isolateNewCmd is the seam between the isolate domain core (durable partial success,
// DESIGN §6.3) and the envelope contract. Run (a) loads the manifest and resolves every
// requested repo to a RepoSpec — a missing manifest is not_found (+`wi init`), an
// undeclared repo is not_found, a malformed manifest is usage (user-fixable input, exit
// 64, NOT an internal bug); (b) reads the minted op_id from the context so the durable
// IsolateRecord carries the SAME id the envelope reports (CTX-OPID); (c) drives
// isolate.New and maps its Status onto the return convention — complete → a created
// Result, partial → the durable (result, *CommandError{Kind: partial}) carrying per-repo
// detail (decision #D), a held lock → kind=lock_held. It never assembles an envelope or
// chooses an exit code (the pipeline owns that).
type isolateNewCmd struct {
	layout layout.Layout
	git    *git.Git
	task   string
	repos  []string
}

func (c *isolateNewCmd) Run(ctx context.Context) (*Result, error) {
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

	specs := make([]isolate.RepoSpec, 0, len(c.repos))
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
		specs = append(specs, isolate.RepoSpec{Name: r.Name, Base: baseref.Resolve(ctx, c.git, c.layout, r.Name, r.Base)})
	}

	res, err := isolate.New(ctx, c.layout, c.git, c.task, OpIDFrom(ctx), specs)
	if err != nil {
		var he *lock.HeldError
		if errors.As(err, &he) {
			return nil, &CommandError{
				Kind:    contract.KindLockHeld,
				Message: fmt.Sprintf("isolate %q is busy: %v", c.task, err),
				Help:    "another wi operation holds this task's lock; retry when it finishes",
			}
		}
		return nil, fmt.Errorf("isolate new %q: %w", c.task, err)
	}

	repos := make([]contract.RepoResult, len(res.Repos))
	for i, oc := range res.Repos {
		repos[i] = projectRepoOutcome(oc)
	}

	if res.Status == isolate.StatusPartial {
		// Durable partial: a result (what completed) AND a top-level partial error.
		return &Result{Action: contract.ActionCreated, Repos: repos},
			&CommandError{
				Kind:    contract.KindPartial,
				Action:  contract.ActionCreated,
				Message: fmt.Sprintf("isolate %q partially created — see repos[] for per-repo status", c.task),
			}
	}

	return &Result{
		Action: contract.ActionCreated,
		Repos:  repos,
		Next:   []string{fmt.Sprintf("see paths with: wi resolve %s", c.task)},
	}, nil
}

// projectRepoOutcome maps one isolate.RepoOutcome onto the wire RepoResult: a created
// stage → action created (else noop — a repo not attempted after a partial), the worktree
// path and tip sha, and a per-repo error on exactly the repo that failed. Mirror/Branch
// stay empty for v0; refining a per-repo Error.Kind beyond internal awaits the gitexec
// stderr classifier.
func projectRepoOutcome(oc isolate.RepoOutcome) contract.RepoResult {
	action := contract.ActionNoop
	if oc.Stage == state.StageCreated {
		action = contract.ActionCreated
	}
	rr := contract.RepoResult{
		Repo:     oc.Repo,
		Action:   action,
		Worktree: oc.Path,
		SHA:      oc.SHA,
		Stage:    string(oc.Stage),
	}
	if oc.Err != nil {
		rr.Error = &contract.Error{
			Kind:    contract.KindInternal,
			Message: oc.Err.Error(),
			Repo:    oc.Repo,
		}
	}
	return rr
}
