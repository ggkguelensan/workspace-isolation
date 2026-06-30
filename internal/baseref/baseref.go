// Package baseref is the seam that turns a base declared as an ordered candidate
// list (defaults.base / repo.base = ["dev","main"] = "prefer dev, else main",
// DESIGN §1) into the single effective base branch the domain cores consume. The
// cores — isolate, land — keep a plain Base string; the manifest→effective-base
// resolution lives here, above git+layout, exactly as the architecture keeps the
// core decoupled from manifest shape. The CLI commands and crash recovery call
// Resolve once per repo when building their per-repo specs.
//
// Resolution reads the repo's already-materialized SSOT mirror: once cloned with
// --branch <base> it tracks exactly one base branch, so the candidate list read back
// against it deterministically yields that one branch (git.FirstExistingBase). The
// genuine candidate-vs-origin choice happens earlier and elsewhere — once, at first
// sync, under the repo lock, via git.FirstExistingRemoteHead — because before the
// first clone there is no mirror to read. When the mirror cannot be read (not yet
// cloned, or none of the candidates resolve), Resolve optimistically returns the top
// preference candidates[0]; downstream ref resolution then surfaces a clean error
// rather than this seam guessing.
package baseref

import (
	"context"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// Resolve returns the effective base branch for repo: the first candidate that
// exists as a local branch in repo's SSOT mirror, or — when the mirror is absent or
// holds none of them — the top preference candidates[0]. candidates is the validated
// non-empty base list from config; an empty list (a contract violation config.Parse
// rejects) yields "" rather than a panic.
func Resolve(ctx context.Context, g *git.Git, l layout.Layout, repo string, candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	if dir, err := l.Repo(repo); err == nil {
		if branch, _, found, ferr := g.FirstExistingBase(ctx, dir, candidates); ferr == nil && found {
			return branch
		}
	}
	return candidates[0]
}
