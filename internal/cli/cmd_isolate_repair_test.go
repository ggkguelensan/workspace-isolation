package cli_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// Guard CMD-ISOLATE-REPAIR: the `wi isolate repair <task>` handler is the seam where the
// three-way drift reconciler (isolate.Inspect read-only + isolate.Repair action, DESIGN
// §7.4 HEAL-1) meets the envelope contract. The isolate-package guards already prove the
// reconcile MECHANICS (Classify/PlanAction/Repair: re-materialize at the owned marker, drop
// a reclaimed tombstone, heal a lagging stage, HARD-BLOCK an orphan, no resurrection). The
// handler's OWN responsibilities, untested there, are the ones asserted here and NEVER an
// envelope or exit code itself:
//   - real run, fully reconciled → a Result (no error, exit 0); the per-cell effects ride in
//     repos[].action (re-materialize→created), the headline action is noop (a reconcile has
//     no single mutation verb — decision #RP);
//   - real run, ≥1 orphan hard-block → a refusal *CommandError{conflict} (exit 4); the orphan
//     rides in repos[] as a per-repo conflict coded orphan_unexplained (envelopeFor threads
//     Repos, NOT Blocked, onto a failure envelope, so a non-zero exit MUST use repos[]);
//   - --dry-run → a read-only Inspect plan: action read, every reconcilable cell in planned[],
//     every orphan would-block in blocked[], NO top-level error so it stays exit 0
//     (SHAPE-DRYRUN-EXIT0), and NOTHING is mutated on disk or in the registry;
//   - state.ErrNoRecord → not_found + `wi isolate new` hint (the isolate does not exist).
//
// Non-vacuity mutant (registered CMD-REPAIR): in isolateRepairCmd.Run, on RepairBlocked
// return `(result, nil)` instead of `(result, *CommandError{Kind: conflict})` → a blocked
// reconcile is mis-reported as a clean success (exit 0) → TestIsolateRepairBlockedIsConflict
// RED (want *cli.CommandError{conflict}, got nil). Alternate: make the --dry-run branch fall
// through to the mutating Repair path → TestIsolateRepairDryRunDoesNotMutate RED (the missing
// worktree gets re-materialized despite --dry-run).

func isolateRepairFactory(t *testing.T, l layout.Layout, g *git.Git) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l, Git: g})["isolate repair"]
	if !ok {
		t.Fatal("BuildRegistry has no \"isolate repair\" factory")
	}
	return f
}

// dropWorktree removes task/repo's worktree directory out-of-band (an external rm / crash),
// leaving the owned marker intact → a MissingWorktree cell the reconciler re-materializes.
func dropWorktree(t *testing.T, l layout.Layout, task, repo string) string {
	t.Helper()
	wt, err := l.Isolate(task, repo)
	if err != nil {
		t.Fatalf("layout.Isolate(%s,%s): %v", task, repo, err)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("RemoveAll(%s): %v", wt, err)
	}
	return wt
}

// orphanWorktree deletes task/repo's owned marker ref but leaves the worktree on disk → an
// OrphanWorktree cell the reconciler HARD-BLOCKS (orphan_unexplained, never auto-removed).
func orphanWorktree(t *testing.T, l layout.Layout, g *git.Git, ctx context.Context, task, repo string) {
	t.Helper()
	ssot, err := l.Repo(repo)
	if err != nil {
		t.Fatalf("layout.Repo(%s): %v", repo, err)
	}
	if err := g.DeleteOwnedRef(ctx, ssot, task, repo); err != nil {
		t.Fatalf("DeleteOwnedRef(%s/%s): %v", task, repo, err)
	}
}

func planFor(plans []contract.PlanItem, repo string) (contract.PlanItem, bool) {
	for _, p := range plans {
		if p.Repo == repo {
			return p, true
		}
	}
	return contract.PlanItem{}, false
}

func blockFor(blocks []contract.BlockItem, repo string) (contract.BlockItem, bool) {
	for _, b := range blocks {
		if b.Repo == repo {
			return b, true
		}
	}
	return contract.BlockItem{}, false
}

// Real run, fully reconcilable: web's worktree was removed out-of-band (MissingWorktree),
// api is consistent. repair re-materializes web at its owned marker and reports a clean
// Result — headline action noop, web action=created, the worktree back on disk.
func TestIsolateRepairReconcilesMissingWorktree(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "api", "web")
	materializeIsolate(t, l, g, ctx, "feat", "api", "web")
	wt := dropWorktree(t, l, "feat", "web") // web worktree gone, marker survives

	cmd, err := isolateRepairFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(ctx)
	if err != nil {
		t.Fatalf("Run (reconcilable): unexpected error %v", err)
	}
	if res.Action != contract.ActionNoop {
		t.Errorf("headline Action = %q, want %q (reconcile has no single mutation verb)", res.Action, contract.ActionNoop)
	}
	web := rmOutcome(t, res, "web")
	if web.Action != contract.ActionCreated || web.Error != nil {
		t.Errorf("web outcome = %+v, want created/no-error (re-materialized)", web)
	}
	api := rmOutcome(t, res, "api")
	if api.Action != contract.ActionNoop || api.Error != nil {
		t.Errorf("api outcome = %+v, want noop/no-error (already consistent)", api)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("web worktree must be re-materialized on disk, stat = %v", err)
	}
}

// Real run with an orphan: web's marker was deleted but its worktree remains (a worktree wi
// cannot prove it owns). repair HARD-BLOCKS it — a conflict refusal (exit 4) carrying the
// orphan in repos[] as a per-repo conflict coded orphan_unexplained. NOT auto-removed.
func TestIsolateRepairBlockedIsConflict(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "web")
	materializeIsolate(t, l, g, ctx, "feat", "web")
	orphanWorktree(t, l, g, ctx, "feat", "web") // marker gone, worktree remains → orphan

	cmd, err := isolateRepairFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(ctx)

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("orphan: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindConflict {
		t.Errorf("Kind = %q, want %q (an orphan hard-block is a conflict refusal)", ce.Kind, contract.KindConflict)
	}
	if res == nil {
		t.Fatal("a blocked reconcile MUST carry a result so the orphan rides in repos[]")
	}
	web := rmOutcome(t, res, "web")
	if web.Error == nil {
		t.Fatalf("orphan repo must carry a per-repo error, got %+v", web)
	}
	if web.Error.Kind != contract.KindConflict {
		t.Errorf("web error kind = %q, want %q", web.Error.Kind, contract.KindConflict)
	}
	if web.Error.Code != "orphan_unexplained" {
		t.Errorf("web error code = %q, want orphan_unexplained", web.Error.Code)
	}
	// The orphan worktree is left intact on disk (never auto-pruned, §7.1).
	wt, _ := l.Isolate("feat", "web")
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("orphan worktree must be left intact, stat = %v", err)
	}
}

// --dry-run: a read-only Inspect plan. web is MissingWorktree (planned: rematerialize),
// db is an orphan (would-block). The plan is exit-neutral (no top-level error despite the
// orphan), action is read, and NOTHING is mutated — web stays missing, db stays present.
func TestIsolateRepairDryRunDoesNotMutate(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "web")
	seedSSOT(t, env, l, g, ctx, "db")
	writeManifest(t, l, "web", "db")
	materializeIsolate(t, l, g, ctx, "feat", "web", "db")
	webWT := dropWorktree(t, l, "feat", "web") // MissingWorktree → planned rematerialize
	orphanWorktree(t, l, g, ctx, "feat", "db") // OrphanWorktree → would-block

	cmd, err := isolateRepairFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithDryRun(ctx, true))
	if err != nil {
		t.Fatalf("--dry-run must not error even with an orphan (exit-neutral), got %v", err)
	}
	if res.Action != contract.ActionRead {
		t.Errorf("dry-run Action = %q, want %q (read-only plan)", res.Action, contract.ActionRead)
	}
	// web's planned reconcile is surfaced in planned[].
	if p, ok := planFor(res.Planned, "web"); !ok {
		t.Errorf("web (missing worktree) must appear in planned[], got %+v", res.Planned)
	} else if p.Action != string(isolate.RepairRematerialize) {
		t.Errorf("web planned action = %q, want %q", p.Action, isolate.RepairRematerialize)
	}
	// db's orphan is surfaced as a would-block in blocked[], not as a top-level error.
	if b, ok := blockFor(res.Blocked, "db"); !ok {
		t.Errorf("db (orphan) must appear in blocked[], got %+v", res.Blocked)
	} else if b.Kind != contract.KindConflict {
		t.Errorf("db block kind = %q, want %q", b.Kind, contract.KindConflict)
	}
	// Read-only: web is still missing and db is still present (nothing was reconciled).
	if _, err := os.Stat(webWT); !os.IsNotExist(err) {
		t.Errorf("--dry-run must NOT re-materialize web, stat err = %v (want not-exist)", err)
	}
	dbWT, _ := l.Isolate("feat", "db")
	if _, err := os.Stat(dbWT); err != nil {
		t.Errorf("--dry-run must leave the orphan intact, stat = %v", err)
	}
}

// A task with no registry record is a not_found refusal hinting at `wi isolate new` — the
// isolate does not exist, distinct from one that exists but has drift.
func TestIsolateRepairMissingRecordIsNotFound(t *testing.T) {
	_, l, g, ctx := isolateEnv(t)

	cmd, err := isolateRepairFactory(t, l, g)([]string{"ghost"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(ctx)
	if res != nil {
		t.Errorf("a missing isolate must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindNotFound {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindNotFound)
	}
	if !contains(ce.Help, "wi isolate new") {
		t.Errorf("missing-isolate help should point at `wi isolate new`, got %q", ce.Help)
	}
}

func TestIsolateRepairFactoryValidatesArgs(t *testing.T) {
	l := bootstrappedLayout(t)
	f := isolateRepairFactory(t, l, git.New(gitexec.New()))

	// no <task> → usage.
	for _, args := range [][]string{nil, {}} {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("args %v: want kind=usage, got %v", args, err)
		}
	}
	// a traversing task name is rejected at the factory.
	if _, err := f([]string{"../evil"}); !isUsage(err) {
		t.Errorf("traversing task name: want kind=usage, got %v", err)
	}
	// repair reconciles the WHOLE isolate (no repo subset), so an extra operand is usage.
	if _, err := f([]string{"feat", "api"}); !isUsage(err) {
		t.Errorf("extra operand: want kind=usage, got %v", err)
	}
	// a bare safe <task> is valid.
	cmd, err := f([]string{"feat"})
	if err != nil || cmd == nil {
		t.Errorf("bare task must build a Command, got cmd=%v err=%v", cmd, err)
	}
}
