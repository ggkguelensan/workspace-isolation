// Package resolve derives the path bundle behind `wi resolve <task>` (and the
// resolve block isolate responses embed) — DESIGN §3.1, §map line 166. It answers
// one question for an agent: "given a task, where is everything?" — the isolate
// root, the runtime state dir, the log location, and per repo the worktree and its
// SSOT mirror.
//
// Bundle is a PURE projection of a layout plus a persisted state record: it does
// ZERO I/O (no filesystem reads, no git, no network — stronger than mirror's
// offline read path, which at least reads a file). Every path comes from
// internal/layout, the sole owner of wi's path scheme (DESIGN §1, §4) — resolve
// never assembles a path itself. The CLI owns loading the record (state.Load) and
// mapping a never-created task (state.ErrNoRecord) to a not_found envelope; Bundle
// takes the already-loaded record so it stays a total, testable function.
package resolve

import (
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Bundle projects rec onto a contract.ResolveBlock using l for every path. It
// iterates rec.Repos (the isolate's actual materialized contents, a subset of the
// manifest chosen at `isolate new` time), preserving their recorded order. It
// errors only if a persisted name fails layout's traversal validation (defense —
// a name that reached the registry already passed it).
//
// Per-repo branch is empty in v0: isolate worktrees are DETACHED (DESIGN §5 — the
// SSOT base ref is never checked out in a worktree, and there are no stray feature
// branches), so a repo has no working branch to report. It is populated once the
// per-repo base is persisted in the state record (a recorded follow-on).
func Bundle(l layout.Layout, rec state.IsolateRecord) (contract.ResolveBlock, error) {
	isolateRoot, err := l.TaskDir(rec.Task)
	if err != nil {
		return contract.ResolveBlock{}, err
	}

	repos := make([]contract.ResolveRepo, 0, len(rec.Repos))
	for _, rr := range rec.Repos {
		worktree, err := l.Isolate(rec.Task, rr.Repo)
		if err != nil {
			return contract.ResolveBlock{}, err
		}
		mirror, err := l.Repo(rr.Repo)
		if err != nil {
			return contract.ResolveBlock{}, err
		}
		repos = append(repos, contract.ResolveRepo{
			Repo:     rr.Repo,
			Worktree: worktree,
			Mirror:   mirror,
			Branch:   "", // v0: detached worktree — no working branch (DESIGN §5)
		})
	}

	return contract.ResolveBlock{
		IsolateRoot: isolateRoot,
		StateDir:    l.StateDir(),
		Log:         l.LogDir(),
		Repos:       repos,
	}, nil
}
