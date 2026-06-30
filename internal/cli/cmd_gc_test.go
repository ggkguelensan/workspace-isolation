package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard CMD-GC: the `wi gc [--dry-run]` handler is the seam where the workspace-wide
// evidence-positive reclamation sweep (gc.Inspect read-only + gc.Collect action, DESIGN
// §7.1/§7.5 HEAL-2) meets the envelope contract. The gc-package guards already prove the
// sweep MECHANICS (Classify/Inspect/Collect: reclaim only the owned-clean-not-ahead-not-live
// cell, preserve blocked_work + orphan_unexplained + live, skip a busy task). The handler's
// OWN responsibilities, untested there, are asserted here and NEVER an envelope or exit code
// itself:
//   - real run, mixed workspace → a refusal *CommandError{conflict} (exit 4); the reclaimed
//     cell rides in repos[].action=removed, every blocked/orphan cell rides in repos[] as a
//     per-cell error sub-coded blocked_work / orphan_unexplained, the live cell is OMITTED
//     (not gc's to touch), and the headline action is noop (a sweep has no single verb —
//     mirror of decision #RP). Each cell's identity is the composite "<task>/<repo>" in the
//     frozen single-string repo field (decision #GC-ID — gc is the one workspace-wide verb);
//   - --dry-run → a read-only Inspect plan: action read, every reclaimable cell in planned[],
//     every blocked/orphan cell would-block in blocked[], NO top-level error so it stays
//     exit 0 (SHAPE-DRYRUN-EXIT0), and NOTHING is mutated on disk or in any marker;
//   - a busy (lock-held) task with no other block → a lock_held refusal (exit 6, not exit 4):
//     a sweep blocked only because an op is in flight is transient retry-later contention,
//     distinct from a deliberate-intervention conflict (decision #GC-EXIT);
//   - `wi gc` takes no operands — an extra arg is a clean usage refusal;
//   - an empty/clean workspace → a clean noop success (exit 0), nothing to reclaim.
//
// Non-vacuity mutant (registered CMD-GC): in gcCmd.collect, on a conflict-blocked sweep
// return (result, nil) instead of (result, *CommandError{conflict}) → a blocked gc is
// mis-reported as a clean success (exit 0) → TestGCReclaimsAndRefuses RED (want
// *cli.CommandError{conflict}, got nil). Alternate: make the --dry-run branch fall through
// to the mutating Collect path → TestGCDryRunDoesNotMutate RED (the reclaimable worktree
// gets removed despite --dry-run).

func gcFactory(t *testing.T, l layout.Layout, g *git.Git) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l, Git: g})["gc"]
	if !ok {
		t.Fatal(`BuildRegistry has no "gc" factory`)
	}
	return f
}

// buildGCWorkspace materializes the four-class workspace the gc-package fitnesses use, but
// through the REAL `isolate new` handler: a live isolate "active" (api), and a de-registered
// "gone" isolate whose worktrees linger as not-live leftovers — one reclaimable (web), two
// blocked (db dirty, ledger ahead of base), one orphan (auth, marker deleted).
func buildGCWorkspace(t *testing.T, env *testenv.Env, l layout.Layout, g *git.Git, ctx context.Context) {
	t.Helper()
	repos := []string{"api", "web", "db", "ledger", "auth"}
	for _, r := range repos {
		seedSSOT(t, env, l, g, ctx, r)
	}
	writeManifest(t, l, repos...)
	materializeIsolate(t, l, g, ctx, "active", "api")
	materializeIsolate(t, l, g, ctx, "gone", "web", "db", "ledger", "auth")
	// De-register "gone" → its worktrees + markers become not-live leftovers.
	if err := state.Delete(l.StateDir(), "gone"); err != nil {
		t.Fatalf("state.Delete(gone): %v", err)
	}
	// gone/db → dirty (uncommitted change).
	dbWT, _ := l.Isolate("gone", "db")
	if err := os.WriteFile(filepath.Join(dbWT, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatalf("dirty db worktree: %v", err)
	}
	// gone/ledger → ahead (a local commit moves HEAD past the owned marker).
	ledgerWT, _ := l.Isolate("gone", "ledger")
	if err := os.WriteFile(filepath.Join(ledgerWT, "feature.txt"), []byte("done"), 0o644); err != nil {
		t.Fatalf("write ledger file: %v", err)
	}
	env.Git(t, ledgerWT, "add", "feature.txt")
	env.Git(t, ledgerWT, "commit", "-m", "local work")
	// gone/auth → orphan (delete the owned marker; the worktree stays).
	authSSOT, _ := l.Repo("auth")
	if err := g.DeleteOwnedRef(ctx, authSSOT, "gone", "auth"); err != nil {
		t.Fatalf("DeleteOwnedRef(auth): %v", err)
	}
}

// Real run over the mixed workspace: gc reclaims ONLY gone/web (owned, clean, not-ahead,
// not-live), HARD-BLOCKS the dirty/ahead/orphan cells, leaves the live cell alone, and
// returns a conflict refusal carrying the per-cell verdicts in repos[].
func TestGCReclaimsAndRefuses(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	buildGCWorkspace(t, env, l, g, ctx)

	cmd, err := gcFactory(t, l, g)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(ctx)

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("mixed workspace: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindConflict {
		t.Errorf("Kind = %q, want %q (a blocked_work/orphan cell is a conflict refusal)", ce.Kind, contract.KindConflict)
	}
	if res == nil {
		t.Fatal("a blocked sweep MUST carry a result so the per-cell verdicts ride in repos[]")
	}
	if res.Action != contract.ActionNoop {
		t.Errorf("headline Action = %q, want %q (a sweep has no single mutation verb)", res.Action, contract.ActionNoop)
	}

	// repos[] projection: web reclaimed (removed); db/ledger blocked_work; auth orphan; the
	// live api cell is NOT gc's concern and must not appear at all.
	web := rmOutcome(t, res, "gone/web")
	if web.Action != contract.ActionRemoved || web.Error != nil {
		t.Errorf("gone/web outcome = %+v, want removed/no-error (reclaimed)", web)
	}
	for _, tc := range []struct{ id, code string }{
		{"gone/db", "blocked_work"},
		{"gone/ledger", "blocked_work"},
		{"gone/auth", "orphan_unexplained"},
	} {
		rr := rmOutcome(t, res, tc.id)
		if rr.Action != contract.ActionNoop || rr.Error == nil {
			t.Errorf("%s outcome = %+v, want noop with a per-cell error", tc.id, rr)
			continue
		}
		if rr.Error.Kind != contract.KindConflict {
			t.Errorf("%s error kind = %q, want %q", tc.id, rr.Error.Kind, contract.KindConflict)
		}
		if rr.Error.Code != tc.code {
			t.Errorf("%s error code = %q, want %q", tc.id, rr.Error.Code, tc.code)
		}
	}
	for _, rr := range res.Repos {
		if rr.Repo == "active/api" {
			t.Errorf("live cell active/api must not appear in gc repos[] (not gc's to touch)")
		}
	}

	// Physical effect: ONLY gone/web's worktree + marker are gone; everything else survives.
	webWT, _ := l.Isolate("gone", "web")
	if _, err := os.Stat(webWT); !os.IsNotExist(err) {
		t.Errorf("gone/web worktree must be reclaimed (removed), stat err = %v (want not-exist)", err)
	}
	if markerPresent(t, ctx, g, l, "gone", "web") {
		t.Errorf("gone/web owned marker must be deleted on reclaim, still present")
	}
	for _, cell := range []struct{ task, repo string }{
		{"gone", "db"}, {"gone", "ledger"}, {"gone", "auth"}, {"active", "api"},
	} {
		wt, _ := l.Isolate(cell.task, cell.repo)
		if _, err := os.Stat(wt); err != nil {
			t.Errorf("%s/%s worktree must be left intact, stat = %v", cell.task, cell.repo, err)
		}
	}
	if !markerPresent(t, ctx, g, l, "gone", "db") || !markerPresent(t, ctx, g, l, "active", "api") {
		t.Errorf("blocked-work and live markers must be preserved (refs/wi/owned protected)")
	}
}

// --dry-run: a read-only Inspect plan. gone/web is planned (reclaim); db/ledger/auth are
// would-blocks; the plan is exit-neutral (no top-level error), action is read, and NOTHING
// is mutated — gone/web's worktree and marker both survive.
func TestGCDryRunDoesNotMutate(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	buildGCWorkspace(t, env, l, g, ctx)

	cmd, err := gcFactory(t, l, g)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithDryRun(ctx, true))
	if err != nil {
		t.Fatalf("--dry-run must not error even with blocked cells (exit-neutral), got %v", err)
	}
	if res.Action != contract.ActionRead {
		t.Errorf("dry-run Action = %q, want %q (read-only plan)", res.Action, contract.ActionRead)
	}
	if p, ok := planFor(res.Planned, "gone/web"); !ok {
		t.Errorf("gone/web (reclaimable) must appear in planned[], got %+v", res.Planned)
	} else if p.Action != "reclaim" {
		t.Errorf("gone/web planned action = %q, want %q", p.Action, "reclaim")
	}
	for _, id := range []string{"gone/db", "gone/ledger", "gone/auth"} {
		if b, ok := blockFor(res.Blocked, id); !ok {
			t.Errorf("%s must appear in blocked[], got %+v", id, res.Blocked)
		} else if b.Kind != contract.KindConflict {
			t.Errorf("%s block kind = %q, want %q", id, b.Kind, contract.KindConflict)
		}
	}
	// The live cell is neither planned nor blocked.
	if _, ok := planFor(res.Planned, "active/api"); ok {
		t.Errorf("live cell active/api must not be planned")
	}
	if _, ok := blockFor(res.Blocked, "active/api"); ok {
		t.Errorf("live cell active/api must not be blocked")
	}
	// Read-only: gone/web is still on disk and its marker still present.
	webWT, _ := l.Isolate("gone", "web")
	if _, err := os.Stat(webWT); err != nil {
		t.Errorf("--dry-run must NOT reclaim gone/web, stat = %v", err)
	}
	if !markerPresent(t, ctx, g, l, "gone", "web") {
		t.Errorf("--dry-run must leave gone/web's marker intact")
	}
}

// A busy task (its isolate-state lock held by an in-flight op) with no other block → a
// lock_held refusal (exit 6), NOT a conflict (exit 4): the sweep is blocked only by transient
// contention. The busy task's worktree is left untouched.
func TestGCBusyTaskIsLockHeld(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "web")
	materializeIsolate(t, l, g, ctx, "gone", "web")
	if err := state.Delete(l.StateDir(), "gone"); err != nil {
		t.Fatalf("state.Delete(gone): %v", err)
	}
	// Hold the task's lock to simulate an in-flight isolate op.
	key, err := lock.IsolateState("gone")
	if err != nil {
		t.Fatalf("lock.IsolateState: %v", err)
	}
	held, err := lock.Acquire(l.LocksDir(), key)
	if err != nil {
		t.Fatalf("Acquire(gone): %v", err)
	}
	defer func() { _ = held.Release() }()

	cmd, err := gcFactory(t, l, g)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(ctx)

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("busy task: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindLockHeld {
		t.Errorf("Kind = %q, want %q (a busy-only sweep is transient lock contention, not a conflict)", ce.Kind, contract.KindLockHeld)
	}
	if res == nil {
		t.Fatal("a busy sweep MUST carry a result so the skipped task rides in repos[]")
	}
	webWT, _ := l.Isolate("gone", "web")
	if _, err := os.Stat(webWT); err != nil {
		t.Errorf("gone/web worktree must be left intact while the task lock is held, stat = %v", err)
	}
	if !markerPresent(t, ctx, g, l, "gone", "web") {
		t.Errorf("gone/web marker must be left intact while the task lock is held")
	}
}

func TestGCFactoryRejectsOperands(t *testing.T) {
	l := bootstrappedLayout(t)
	f := gcFactory(t, l, git.New(gitexec.New()))

	// gc is workspace-wide and takes no operands.
	if _, err := f([]string{"some-task"}); !isUsage(err) {
		t.Errorf("operand: want kind=usage, got %v", err)
	}
	// Bare `wi gc` builds a Command.
	cmd, err := f(nil)
	if err != nil || cmd == nil {
		t.Errorf("bare `wi gc` must build a Command, got cmd=%v err=%v", cmd, err)
	}
}

// An empty workspace (no isolate ever created) is a clean noop success — gc has nothing to
// reclaim, the idempotent posture every reclamation verb takes.
func TestGCEmptyWorkspaceIsCleanNoop(t *testing.T) {
	_, l, g, ctx := isolateEnv(t)

	cmd, err := gcFactory(t, l, g)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(ctx)
	if err != nil {
		t.Fatalf("empty workspace: unexpected error %v", err)
	}
	if res.Action != contract.ActionNoop {
		t.Errorf("Action = %q, want %q", res.Action, contract.ActionNoop)
	}
	if len(res.Repos) != 0 {
		t.Errorf("empty workspace must reclaim nothing, got repos %+v", res.Repos)
	}
}

// markerPresent reports whether the wi-owned marker ref for task/repo still exists.
func markerPresent(t *testing.T, ctx context.Context, g *git.Git, l layout.Layout, task, repo string) bool {
	t.Helper()
	ssot, err := l.Repo(repo)
	if err != nil {
		t.Fatalf("layout.Repo(%s): %v", repo, err)
	}
	_, exists, err := g.OwnedRefSHA(ctx, ssot, task, repo)
	if err != nil {
		t.Fatalf("OwnedRefSHA(%s/%s): %v", task, repo, err)
	}
	return exists
}
