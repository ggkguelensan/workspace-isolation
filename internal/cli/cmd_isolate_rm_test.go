package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// Guard CMD-ISOLATE-RM: the `wi isolate rm <task> [<repo>…]` handler is the teardown seam
// where the evidence-positive reclamation core (isolate.Remove) meets the envelope
// contract. The isolate-package guards already prove the GATES (orphan hard-block, clean
// reclaim, registry teardown); the handler's OWN responsibilities, untested there, are the
// outcome→envelope mapping it must get right and NEVER an envelope or exit code itself:
//   - RemoveComplete            → a removed Result (exit 0);
//   - mixed (some reclaimed,
//     some blocked)             → the DURABLE PARTIAL (result, *CommandError{partial,
//                                  Action: removed}) carrying per-repo detail (decision #D),
//                                  because re-running reclaims any now-unblocked repos;
//   - nothing reclaimed,
//     ≥1 orphan hard-block      → a full refusal *CommandError{conflict} (exit 4) — the
//                                  on-disk state conflicts with what wi can prove it owns;
//   - state.ErrNoRecord         → not_found + `wi isolate new` hint (the isolate does not
//                                  exist), *lock.HeldError → lock_held (decision #RD).
// Blocked repos ride in repos[] as per-repo Errors (code orphan_unexplained), NOT Blocked[]:
// envelopeFor does not thread Blocked[] on the error/partial path, so a non-zero exit must
// surface them in repos[] or they vanish.
//
// Non-vacuity mutant (registered): in isolateRmCmd.Run, on the mixed outcome return
// `(result, nil)` instead of `(result, *CommandError{Kind: partial})` → a partial teardown
// is mis-reported as a clean success → TestIsolateRmDurablePartialBlocksOrphan RED (want
// *cli.CommandError{partial}, got nil). Alternate: map a blocked repo's per-repo Error.Kind
// to something other than conflict / drop its orphan_unexplained code → same test RED on
// the repos[] assertions.

func isolateRmFactory(t *testing.T, l layout.Layout, g *git.Git) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l, Git: g})["isolate rm"]
	if !ok {
		t.Fatal("BuildRegistry has no \"isolate rm\" factory")
	}
	return f
}

// materializeIsolate drives the real `isolate new` handler to create task over repos, so
// the rm tests start from a genuine, marker-stamped isolate (not a hand-faked record).
func materializeIsolate(t *testing.T, l layout.Layout, g *git.Git, ctx context.Context, task string, repos ...string) {
	t.Helper()
	cmd, err := isolateNewFactory(t, l, g)(append([]string{task}, repos...))
	if err != nil {
		t.Fatalf("isolate new factory: %v", err)
	}
	if _, err := cmd.Run(cli.WithOpID(ctx, "op_seed_"+task)); err != nil {
		t.Fatalf("materialize isolate %q: %v", task, err)
	}
}

// moveAhead adds a local commit in task/repo's worktree so its HEAD moves past the owned
// marker (the base tip at creation) — making it "ahead of base", an unexplained orphan the
// reclamation gates HARD BLOCK (clean tree, but it carries local work). DESIGN §7.1.
func moveAhead(t *testing.T, env interface {
	Git(t *testing.T, dir string, args ...string) string
}, l layout.Layout, task, repo string) {
	t.Helper()
	wt, err := l.Isolate(task, repo)
	if err != nil {
		t.Fatalf("layout.Isolate(%s,%s): %v", task, repo, err)
	}
	env.Git(t, wt, "commit", "--allow-empty", "-m", "local work")
}

func rmOutcome(t *testing.T, res *cli.Result, repo string) contract.RepoResult {
	t.Helper()
	for _, rr := range res.Repos {
		if rr.Repo == repo {
			return rr
		}
	}
	t.Fatalf("repo %q not present in result repos %+v", repo, res.Repos)
	return contract.RepoResult{}
}

// Complete teardown: every recorded repo passes the gates, so rm reports a removed Result
// (no error) with each repo action=removed, and the registry record is gone.
func TestIsolateRmCompleteRemovesAll(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "api", "web")
	materializeIsolate(t, l, g, ctx, "feat", "api", "web")

	// nil repo subset → full teardown.
	cmd, err := isolateRmFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_rm_test"))
	if err != nil {
		t.Fatalf("Run (complete): unexpected error %v", err)
	}
	if res.Action != contract.ActionRemoved {
		t.Errorf("Action = %q, want %q", res.Action, contract.ActionRemoved)
	}
	for _, repo := range []string{"api", "web"} {
		rr := rmOutcome(t, res, repo)
		if rr.Action != contract.ActionRemoved || rr.Error != nil {
			t.Errorf("repo %s outcome = %+v, want removed/no-error", repo, rr)
		}
	}
	if _, err := state.Load(l.StateDir(), "feat"); !errors.Is(err, state.ErrNoRecord) {
		t.Errorf("record must be deleted after full reclaim, got %v", err)
	}
}

// Durable partial (decision #D + #RD): web is ahead of base (an orphan hard-block) while api
// reclaims. The handler maps the mixed outcome onto BOTH a removed Result AND a
// *CommandError{partial}; the blocked repo rides in repos[] as a per-repo conflict error
// coded orphan_unexplained, and api is dropped from the still-present record (resumable).
func TestIsolateRmDurablePartialBlocksOrphan(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "api", "web")
	materializeIsolate(t, l, g, ctx, "feat", "api", "web")
	moveAhead(t, env, l, "feat", "web") // web now carries local work → ahead of base

	cmd, err := isolateRmFactory(t, l, g)([]string{"feat", "api", "web"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_rm_test"))

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("mixed: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindPartial {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindPartial)
	}
	if ce.Action != contract.ActionRemoved {
		t.Errorf("partial CommandError.Action = %q, want %q (the verb in flight)", ce.Action, contract.ActionRemoved)
	}
	if res == nil {
		t.Fatal("a durable partial MUST carry a result alongside the error (decision #D)")
	}

	api := rmOutcome(t, res, "api")
	if api.Action != contract.ActionRemoved || api.Error != nil {
		t.Errorf("api outcome = %+v, want removed/no-error", api)
	}
	web := rmOutcome(t, res, "web")
	if web.Error == nil {
		t.Fatalf("web (ahead of base) must carry a per-repo error, got %+v", web)
	}
	if web.Error.Kind != contract.KindConflict {
		t.Errorf("web error kind = %q, want %q", web.Error.Kind, contract.KindConflict)
	}
	if web.Error.Code != "orphan_unexplained" {
		t.Errorf("web error code = %q, want orphan_unexplained", web.Error.Code)
	}

	// Resumable: only the blocked repo remains in the record.
	rec, err := state.Load(l.StateDir(), "feat")
	if err != nil {
		t.Fatalf("state.Load after partial: %v", err)
	}
	if len(rec.Repos) != 1 || rec.Repos[0].Repo != "web" {
		t.Errorf("record after partial = %+v, want only [web] (api reclaimed)", rec.Repos)
	}
}

// Nothing reclaimed because the sole repo is an orphan hard-block → a full refusal mapped to
// conflict (exit 4), NOT partial: no durable progress was made, so it is a clean refusal the
// agent must resolve. The blocked repo still rides in repos[].
func TestIsolateRmAllBlockedIsConflict(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "web")
	materializeIsolate(t, l, g, ctx, "feat", "web")
	moveAhead(t, env, l, "feat", "web")

	cmd, err := isolateRmFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_rm_test"))

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("all-blocked: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindConflict {
		t.Errorf("Kind = %q, want %q (a full refusal, nothing reclaimed)", ce.Kind, contract.KindConflict)
	}
	if res == nil || rmOutcome(t, res, "web").Error == nil {
		t.Errorf("the blocked repo must still ride in repos[], got res=%+v", res)
	}
	// The record is untouched (nothing reclaimed).
	if rec, err := state.Load(l.StateDir(), "feat"); err != nil || len(rec.Repos) != 1 {
		t.Errorf("record must be intact after an all-blocked refusal, got rec=%+v err=%v", rec, err)
	}
}

// A task with no registry record is a not_found refusal hinting at `wi isolate new` — the
// isolate does not exist, distinct from one that exists but refuses (conflict).
func TestIsolateRmMissingRecordIsNotFound(t *testing.T) {
	_, l, g, ctx := isolateEnv(t)

	cmd, err := isolateRmFactory(t, l, g)([]string{"ghost"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_rm_test"))
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

func TestIsolateRmFactoryValidatesArgs(t *testing.T) {
	l := bootstrappedLayout(t)
	f := isolateRmFactory(t, l, git.New(gitexec.New()))

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
	// a bare safe <task> (full teardown, no repo subset) is valid.
	cmd, err := f([]string{"feat"})
	if err != nil || cmd == nil {
		t.Errorf("bare task must build a Command (full teardown), got cmd=%v err=%v", cmd, err)
	}
}
