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
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard CMD-LAND: the `wi land <task> <repo>…` handler is the seam where the land domain
// (land.RunJournaled — durable, journaled, stop-at-first-block, DESIGN §7.2/§7.4) meets the
// envelope contract. The land/landstate guards already prove the git-level safety (ff-only
// base advance, backup-before-move, non-ff → parked block) and the journal lifecycle; the
// handler's OWN responsibilities, untested there, are spec resolution from the manifest and
// the Status→envelope/return mapping it must get right (it NEVER assembles an envelope or
// chooses an exit code — the pipeline owns that):
//   - StatusLanded (all repos ff'd)        → a landed Result (exit 0);
//   - mixed (some landed, ≥1 blocked)       → the DURABLE PARTIAL (result, *CommandError{
//                                              partial, Action: landed}) — durable progress
//                                              made, `land continue` resumes the rest (exit 2);
//   - nothing landed, ≥1 blocked            → a full refusal *CommandError{conflict} (exit 4):
//                                              no base moved, the agent must rebase then retry;
//   - a held lock                           → lock_held (exit 6); a missing/undeclared repo →
//                                              not_found; a malformed manifest → usage.
// A blocked repo rides in repos[] as a per-repo Error (a clean non-ff → conflict coded
// non_fast_forward so the agent knows to rebase; an infra fault → internal), NOT Blocked[]:
// envelopeFor does not thread Blocked[] on the error path, so a non-zero exit must surface
// the blocks in repos[] or they vanish.
//
// Non-vacuity mutant (registered): in landCmd.Run, on the mixed outcome return (result, nil)
// instead of (result, *CommandError{partial}) → a partial land is mis-reported as a clean
// success → TestLandCommandPartialBlocksOneRepo RED (want *cli.CommandError{partial}, got
// nil). Alternate: map the all-blocked outcome to KindPartial instead of KindConflict →
// TestLandCommandAllBlockedIsConflict RED (a nothing-changed refusal must be exit 4, not 2).

func landFactory(t *testing.T, l layout.Layout, g *git.Git) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l, Git: g})["land"]
	if !ok {
		t.Fatal("BuildRegistry has no \"land\" factory")
	}
	return f
}

// landWork adds a commit in task/repo's worktree so its HEAD moves past the base tip — a
// descendant commit the base can fast-forward to (the agent's "work tip").
func landWork(t *testing.T, env *testenv.Env, l layout.Layout, task, repo string) {
	t.Helper()
	wt, err := l.Isolate(task, repo)
	if err != nil {
		t.Fatalf("layout.Isolate(%s,%s): %v", task, repo, err)
	}
	env.Git(t, wt, "commit", "--allow-empty", "-m", "agent work in "+repo)
}

// divergeBase moves repo's SSOT base to a COMPETING commit (via a throwaway second worktree
// off the base), so the isolate's work tip is no longer a fast-forward — landing it must
// REFUSE and park blocked.
func divergeBase(t *testing.T, env *testenv.Env, l layout.Layout, g *git.Git, ctx context.Context, repo string) {
	t.Helper()
	ssot, err := l.Repo(repo)
	if err != nil {
		t.Fatalf("layout.Repo(%s): %v", repo, err)
	}
	other := env.Root + "/diverge-" + repo
	if err := g.AddWorktree(ctx, ssot, other, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree(diverge %s): %v", repo, err)
	}
	env.Git(t, other, "commit", "--allow-empty", "-m", "competing land in "+repo)
	divergent := env.Git(t, other, "rev-parse", "HEAD")
	env.Git(t, ssot, "update-ref", "refs/heads/"+testenv.DefaultBranch, divergent)
}

func landOutcome(t *testing.T, res *cli.Result, repo string) contract.RepoResult {
	t.Helper()
	for _, rr := range res.Repos {
		if rr.Repo == repo {
			return rr
		}
	}
	t.Fatalf("repo %q not present in result repos %+v", repo, res.Repos)
	return contract.RepoResult{}
}

// Happy path: every repo's work tip fast-forwards onto its base, so land reports a landed
// Result (exit 0) with each repo action=landed carrying the landed sha, and the SSOT base
// has advanced to the work tip.
func TestLandCommandLandsAllRepos(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "api", "web")
	materializeIsolate(t, l, g, ctx, "feat", "api", "web")
	landWork(t, env, l, "feat", "api")
	landWork(t, env, l, "feat", "web")

	cmd, err := landFactory(t, l, g)([]string{"feat", "api", "web"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_land_cli"))
	if err != nil {
		t.Fatalf("Run (all landed): unexpected error %v", err)
	}
	if res.Action != contract.ActionLanded {
		t.Errorf("Action = %q, want %q", res.Action, contract.ActionLanded)
	}
	for _, repo := range []string{"api", "web"} {
		rr := landOutcome(t, res, repo)
		if rr.Action != contract.ActionLanded || rr.Error != nil {
			t.Errorf("repo %s outcome = %+v, want landed/no-error", repo, rr)
		}
		if rr.SHA == "" {
			t.Errorf("repo %s: landed sha must be populated", repo)
		}
		// The SSOT base advanced to the landed work tip.
		wt, _ := l.Isolate("feat", repo)
		workTip := env.Git(t, wt, "rev-parse", "HEAD")
		ssot, _ := l.Repo(repo)
		if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != workTip {
			t.Errorf("%s base = %q, want advanced to work tip %q", repo, got, workTip)
		}
		if rr.SHA != workTip {
			t.Errorf("%s landed sha = %q, want work tip %q", repo, rr.SHA, workTip)
		}
	}
}

// Durable partial: api lands, but web's base diverged so web refuses — the run stops with
// durable progress (api's base moved), mapped to BOTH a landed Result AND a
// *CommandError{partial}. The blocked web rides in repos[] as a conflict coded
// non_fast_forward.
func TestLandCommandPartialBlocksOneRepo(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "api", "web")
	materializeIsolate(t, l, g, ctx, "feat", "api", "web")
	landWork(t, env, l, "feat", "api")
	landWork(t, env, l, "feat", "web")
	divergeBase(t, env, l, g, ctx, "web") // web is now a non-fast-forward

	cmd, err := landFactory(t, l, g)([]string{"feat", "api", "web"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_land_cli"))

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("mixed: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindPartial {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindPartial)
	}
	if ce.Action != contract.ActionLanded {
		t.Errorf("partial CommandError.Action = %q, want %q (the verb in flight)", ce.Action, contract.ActionLanded)
	}
	if res == nil {
		t.Fatal("a durable partial MUST carry a result alongside the error")
	}

	api := landOutcome(t, res, "api")
	if api.Action != contract.ActionLanded || api.Error != nil {
		t.Errorf("api outcome = %+v, want landed/no-error", api)
	}
	web := landOutcome(t, res, "web")
	if web.Error == nil {
		t.Fatalf("web (non-ff) must carry a per-repo error, got %+v", web)
	}
	if web.Error.Kind != contract.KindConflict {
		t.Errorf("web error kind = %q, want %q", web.Error.Kind, contract.KindConflict)
	}
	if web.Error.Code != "non_fast_forward" {
		t.Errorf("web error code = %q, want non_fast_forward", web.Error.Code)
	}
}

// Nothing landed: the sole repo diverged, so land refuses with NOTHING moved → a full
// conflict refusal (exit 4), NOT partial. The blocked repo still rides in repos[].
func TestLandCommandAllBlockedIsConflict(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "web")
	materializeIsolate(t, l, g, ctx, "feat", "web")
	landWork(t, env, l, "feat", "web")
	divergeBase(t, env, l, g, ctx, "web")

	cmd, err := landFactory(t, l, g)([]string{"feat", "web"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_land_cli"))

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("all-blocked: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindConflict {
		t.Errorf("Kind = %q, want %q (a full refusal, nothing landed)", ce.Kind, contract.KindConflict)
	}
	if res == nil || landOutcome(t, res, "web").Error == nil {
		t.Errorf("the blocked repo must still ride in repos[], got res=%+v", res)
	}
}

// A task with no isolate record cannot land — the worktree path resolution still works
// (layout is pure), but there is nothing committed to land and the base cannot advance;
// here we only assert arg validation at the factory (mirrors isolate new/rm).
func TestLandCommandFactoryValidatesArgs(t *testing.T) {
	l := bootstrappedLayout(t)
	f := landFactory(t, l, git.New(gitexec.New()))

	for _, args := range [][]string{nil, {}, {"feat"}} {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("args %v: want kind=usage, got %v", args, err)
		}
	}
	if _, err := f([]string{"../evil", "api"}); !isUsage(err) {
		t.Errorf("traversing task name: want kind=usage, got %v", err)
	}
	cmd, err := f([]string{"feat", "api"})
	if err != nil || cmd == nil {
		t.Errorf("valid args must build a Command, got cmd=%v err=%v", cmd, err)
	}
}
