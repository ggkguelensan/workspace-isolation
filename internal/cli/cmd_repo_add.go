package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/ggkguelensan/workspace-isolation/internal/config"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// newRepoAddCommand is the `wi repo add <name> <url> [--base <branch>]` factory. Dispatch
// has already stripped the global flags (--dry-run/--format), so the only flag left to parse
// here is the command-specific --base (both `--base v` and `--base=v` forms). After the flag
// is extracted, EXACTLY two positionals must remain — <name> <url>; anything else is a usage
// refusal. <name> is validated through layout.ValidateSegment HERE (it becomes a path
// segment under repos/<name>), so a traversing name is a clean usage refusal before any I/O,
// mirroring how `isolate new` validates its <task> in the factory.
func newRepoAddCommand(l layout.Layout, args []string) (Command, error) {
	base, rest, err := extractBaseFlag(args)
	if err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	if len(rest) != 2 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi repo add <name> <url> [--base <branch>]"}
	}
	name, url := rest[0], rest[1]
	if err := layout.ValidateSegment("repo", name); err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	return &repoAddCmd{layout: l, name: name, url: url, base: base}, nil
}

// extractBaseFlag pulls the optional --base/--base= flag out of args, returning the base
// value ("" if absent) and the remaining positionals in order. A --base with no following
// value is a usage error. Only --base is recognized; any other --flag is left in the
// positionals so the arg-count check rejects it (rather than silently ignoring a typo'd flag).
func extractBaseFlag(args []string) (base string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--base":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--base requires a value (a branch name)")
			}
			base = args[i+1]
			i++
		case len(a) > len("--base=") && a[:len("--base=")] == "--base=":
			base = a[len("--base="):]
		default:
			rest = append(rest, a)
		}
	}
	return base, rest, nil
}

// repoAddCmd appends a repo declaration to the manifest. It is a thin seam over config.Add
// (the AST-preserving edit primitive): the handler owns only the project-registry lock (so a
// registry mutation serializes against concurrent `repo add`s) and the mapping of config.Add's
// outcomes onto the envelope contract. No git, no network — a manifest edit only.
type repoAddCmd struct {
	layout layout.Layout
	name   string
	url    string
	base   string // "" → omit the base field so the repo inherits defaults.base
}

func (c *repoAddCmd) Run(ctx context.Context) (*Result, error) {
	// A repo add mutates the committed registry, so it takes the project-registry lock for
	// the whole edit — the same key v1 uses to serialize every registry mutation. A
	// contended lock is lock_held (exit 6), never a corrupting concurrent rewrite.
	held, err := lock.Acquire(c.layout.LocksDir(), lock.ProjectRegistry())
	if err != nil {
		var he *lock.HeldError
		if errors.As(err, &he) {
			return nil, &CommandError{
				Kind:    contract.KindLockHeld,
				Message: "the workspace registry is busy: " + err.Error(),
				Help:    "another wi operation holds the project-registry lock; retry when it finishes",
			}
		}
		return nil, fmt.Errorf("repo add: acquire registry lock: %w", err)
	}
	defer held.Release()
	// Record who holds the project-registry lock so the self-heal layer can later read
	// the holder and judge staleness (DESIGN §6 / §7.3). Best-effort: the flock is the
	// exclusion guarantee, so a failed metadata write must not abort the edit — a
	// body-less lock reads as "unknown holder" and is conservatively never auto-broken.
	_ = held.Stamp(OpIDFrom(ctx))

	err = config.Add(c.layout.Config(), c.name, c.url, c.base)
	switch {
	case err == nil:
		return &Result{
			Action: contract.ActionCreated,
			Next:   []string{fmt.Sprintf("fetch it as SSOT with: wi sync %s", c.name)},
		}, nil
	case errors.Is(err, config.ErrDuplicateRepo):
		return nil, &CommandError{
			Kind:    contract.KindAlreadyExists,
			Message: fmt.Sprintf("repo %q is already declared in the manifest", c.name),
			Repo:    c.name,
			Help:    "pick a different name, or edit the existing entry in wi.config.jsonc",
		}
	case errors.Is(err, fs.ErrNotExist):
		return nil, &CommandError{
			Kind:    contract.KindNotFound,
			Message: fmt.Sprintf("no wi workspace here: %s not found", c.layout.Config()),
			Help:    "create one with: wi init",
		}
	default:
		// A malformed EXISTING manifest is user-fixable input → usage; config.Add's
		// internal failures (splice/atomic-write) are wrapped distinctly. v0 folds the
		// remainder into usage so a hand-corrupted manifest reports as fixable rather than
		// as a wi bug; refining genuine write faults to internal awaits a typed parse error.
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
}
