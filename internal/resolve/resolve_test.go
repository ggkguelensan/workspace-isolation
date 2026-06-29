package resolve_test

import (
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/resolve"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Guard RESOLVE-BUNDLE: Bundle projects a persisted isolate record onto the
// contract.ResolveBlock, sourcing EVERY path from layout — the isolate root, the
// state dir, the log dir, and per repo the worktree (isolas/<task>/<repo>) and its
// SSOT mirror (repos/<repo>), in recorded order, with v0's empty (detached) branch.
// The expected paths are built by an INDEPENDENT hand-written join of the scheme
// (not by calling the same layout accessors), so a mis-wired projection is caught.
//
// Non-vacuity mutant (registered): wire `mirror` to the worktree path instead of
// layout.Repo — the per-repo SSOT mirror then equals the worktree, reddening the
// Mirror assertions and proving Bundle distinguishes the isolas/<task>/<repo>
// worktree from the repos/<repo> SSOT clone. A second mutant — drop a repo from the
// loop — reddens the count/second-repo assertions, proving every recorded repo is
// projected.
func TestBundleProjectsRecordPaths(t *testing.T) {
	const root = "/wi/project" // absolute; layout.New does pure path joins, no I/O
	l, err := layout.New(root)
	if err != nil {
		t.Fatalf("layout.New: %v", err)
	}

	rec := state.IsolateRecord{
		Task: "feat",
		OpID: "op_test_aaaa",
		Repos: []state.RepoRecord{
			{Repo: "api", Stage: state.StageCreated},
			{Repo: "web", Stage: state.StagePending},
		},
	}

	block, err := resolve.Bundle(l, rec)
	if err != nil {
		t.Fatalf("resolve.Bundle: %v", err)
	}

	// Top-level paths, built independently from the documented scheme.
	if got, want := block.IsolateRoot, filepath.Join(root, "isolas", "feat"); got != want {
		t.Errorf("IsolateRoot = %q, want %q", got, want)
	}
	if got, want := block.StateDir, filepath.Join(root, ".wi", "state"); got != want {
		t.Errorf("StateDir = %q, want %q", got, want)
	}
	if got, want := block.Log, filepath.Join(root, ".wi", "log"); got != want {
		t.Errorf("Log = %q, want %q", got, want)
	}

	// Every recorded repo is projected, in order, with the right worktree/mirror.
	want := []contract.ResolveRepo{
		{
			Repo:     "api",
			Worktree: filepath.Join(root, "isolas", "feat", "api"),
			Mirror:   filepath.Join(root, "repos", "api"),
			Branch:   "",
		},
		{
			Repo:     "web",
			Worktree: filepath.Join(root, "isolas", "feat", "web"),
			Mirror:   filepath.Join(root, "repos", "web"),
			Branch:   "",
		},
	}
	if len(block.Repos) != len(want) {
		t.Fatalf("Repos count = %d, want %d (%+v)", len(block.Repos), len(want), block.Repos)
	}
	for i, w := range want {
		if block.Repos[i] != w {
			t.Errorf("Repos[%d] = %+v, want %+v", i, block.Repos[i], w)
		}
	}
}

// An isolate with no repos still yields a well-formed block whose Repos is an empty
// (non-nil) slice — the envelope's MarshalJSON renders it as [], never null.
func TestBundleEmptyRecordYieldsEmptyRepos(t *testing.T) {
	l, err := layout.New("/wi/project")
	if err != nil {
		t.Fatalf("layout.New: %v", err)
	}
	block, err := resolve.Bundle(l, state.IsolateRecord{Task: "feat", OpID: "op_x"})
	if err != nil {
		t.Fatalf("resolve.Bundle: %v", err)
	}
	if block.Repos == nil {
		t.Error("Repos is nil; want a non-nil empty slice so it marshals as []")
	}
	if len(block.Repos) != 0 {
		t.Errorf("Repos = %+v, want empty", block.Repos)
	}
}

// A persisted name that fails layout's traversal validation surfaces as an error
// (defense in depth — a name that reached the registry already passed validation).
func TestBundleRejectsUnsafeName(t *testing.T) {
	l, err := layout.New("/wi/project")
	if err != nil {
		t.Fatalf("layout.New: %v", err)
	}
	_, err = resolve.Bundle(l, state.IsolateRecord{
		Task:  "feat",
		Repos: []state.RepoRecord{{Repo: "../escape", Stage: state.StageCreated}},
	})
	if err == nil {
		t.Fatal("Bundle with a traversing repo name = nil error, want a validation error")
	}
}
