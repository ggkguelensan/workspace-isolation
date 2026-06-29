// Package isolate is the domain core of `wi isolate new`: it materializes one
// detached worktree per declared repo off the SSOT base, recording progress in
// internal/state as it goes (DESIGN §1, §6.3). It is the partial-success-critical
// command — the one place wi's "durable partial success" contract is enforced.
//
// Orchestration (DESIGN §6.3). Under the isolate-state:<task> lock (DESIGN §6.1),
// New first writes an IsolateRecord with every requested repo at StagePending —
// the durable statement of intent that makes the op resumable — and ONLY THEN
// materializes repos one at a time, in request order. Each repo is, in this exact
// order:
//
//	1. git worktree add --detach   (the linked worktree off refs/heads/<base>)
//	2. CreateOwnedRef              (refs/wi/owned/<task>/<repo> — evidence-positive
//	                                ownership BEFORE we claim the repo "created", so a
//	                                crash after step 1 leaves a wi-owned reclaimable
//	                                worktree, never an unexplained orphan — DESIGN §7.1)
//	3. state.UpdateRepoStage(...Created)   (flip the durable registry to created)
//
// It is STOP-ON-FIRST-FAIL with durable, NOT-rolled-back completed repos: the
// first repo that fails halts the run, every repo completed before it stays on
// disk and in the registry, and repos after it are never attempted (they remain
// StagePending). The result carries StatusPartial so the CLI emits exit 2; the run
// is resumable because the registry reflects EXACTLY the completed set. A per-repo
// failure is NOT a Go error from New — it is recorded in the result; New's error
// return is reserved for failures that prevent the op from running at all (a held
// lock → *lock.HeldError → exit 6, an unwritable initial record, an unsafe name).
//
// SSOT invariants (DESIGN §5): worktrees are added off refs/heads/<base> but the
// base ref is NEVER moved here (only git.FastForwardBaseRef advances a base ref),
// and the SSOT working tree is never dirtied — a linked worktree shares the object
// store without touching the SSOT checkout.
package isolate

import (
	"context"
	"fmt"
	"os"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// RepoSpec is one repo to materialize: its wi-internal name (→ repos/<name> SSOT,
// isolas/<task>/<name> worktree) and the EFFECTIVE base branch the worktree
// detaches at. The CLI resolves these from the manifest (config.Repo) before
// calling New, so this package stays decoupled from manifest parsing.
type RepoSpec struct {
	Name string
	Base string
}

// Status is the overall outcome of a New run.
type Status string

const (
	// StatusComplete: every requested repo was materialized.
	StatusComplete Status = "complete"
	// StatusPartial: the run stopped on the first repo that failed; repos before
	// it are durably complete, repos after it were not attempted (exit 2).
	StatusPartial Status = "partial"
)

// RepoOutcome is one repo's result within a New run. Stage is StageCreated once the
// worktree+marker+registry flip all succeeded, else StagePending (not reached). Err
// is set on exactly the repo that failed (the one that triggered stop-on-first-fail).
type RepoOutcome struct {
	Repo  string
	Base  string
	Stage state.Stage
	Path  string // worktree path (set once the path was computed)
	SHA   string // worktree HEAD sha (= base tip at add) on success
	Err   error
}

// Result is the outcome of a New run: the task/op identity, the overall Status, and
// a per-repo outcome in request order. The CLI projects this onto the envelope's
// repos[] and the exit code (complete → 0, partial → 2).
type Result struct {
	Task   string
	OpID   string
	Status Status
	Repos  []RepoOutcome
}

// New materializes an isolate for task: one detached worktree per repo in specs,
// off each repo's SSOT base, under the isolate-state:<task> lock. See the package
// doc for the durable-partial-success contract. l must be Bootstrap'd (the lock and
// state dirs must exist). opID identifies the creating op in the registry.
func New(ctx context.Context, l layout.Layout, g *git.Git, task, opID string, specs []RepoSpec) (Result, error) {
	key, err := lock.IsolateState(task)
	if err != nil {
		return Result{}, err
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		return Result{}, err // *lock.HeldError → exit 6 (DESIGN §6.1)
	}
	defer func() { _ = held.Release() }()

	// The isolas/<task>/ parent must exist before any worktree leaf is added.
	taskDir, err := l.TaskDir(task)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("isolate: create task dir %s: %w", taskDir, err)
	}

	// Durable statement of intent: every requested repo recorded StagePending
	// BEFORE any materialization, so a crash leaves a resumable registry (§6.3).
	stateDir := l.StateDir()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	if err := state.Store(stateDir, state.NewIsolateRecord(task, opID, names)); err != nil {
		return Result{}, fmt.Errorf("isolate: write initial record for %q: %w", task, err)
	}

	res := Result{Task: task, OpID: opID, Status: StatusComplete, Repos: make([]RepoOutcome, 0, len(specs))}
	for i := range specs {
		s := specs[i]
		oc := RepoOutcome{Repo: s.Name, Base: s.Base, Stage: state.StagePending}

		ssot, err := l.Repo(s.Name)
		if err == nil {
			oc.Path, err = l.Isolate(task, s.Name)
		}
		if err == nil {
			oc.SHA, err = materializeRepo(ctx, g, ssot, oc.Path, task, s.Name, s.Base, stateDir)
		}
		if err != nil {
			// Stop-on-first-fail: record this repo's failure, mark every later repo
			// not-attempted (StagePending), and return a durable partial success.
			oc.Err = err
			res.Status = StatusPartial
			res.Repos = append(res.Repos, oc)
			for _, rest := range specs[i+1:] {
				res.Repos = append(res.Repos, RepoOutcome{Repo: rest.Name, Base: rest.Base, Stage: state.StagePending})
			}
			return res, nil
		}

		oc.Stage = state.StageCreated
		res.Repos = append(res.Repos, oc)
	}
	return res, nil
}

// materializeRepo runs the three-step per-repo materialization in the order the
// evidence-positive contract requires (worktree → marker → registry flip), so any
// crash leaves a wi-owned, reclaimable state rather than an unexplained orphan
// (DESIGN §7.1). It returns the worktree HEAD sha (the base tip at add time) that
// the marker records. It never moves the base ref or dirties the SSOT (DESIGN §5).
func materializeRepo(ctx context.Context, g *git.Git, ssotDir, wtPath, task, repo, base, stateDir string) (string, error) {
	baseRef := "refs/heads/" + base
	sha, err := g.ResolveRef(ctx, ssotDir, baseRef)
	if err != nil {
		return "", err
	}
	if err := g.AddWorktree(ctx, ssotDir, wtPath, baseRef); err != nil {
		return "", err
	}
	if err := g.CreateOwnedRef(ctx, ssotDir, task, repo, sha); err != nil {
		return "", err
	}
	if err := state.UpdateRepoStage(stateDir, task, repo, state.StageCreated); err != nil {
		return "", err
	}
	return sha, nil
}
