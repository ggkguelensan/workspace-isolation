package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard CMD-LAND-ABORT: `wi land abort <task>` is the mutating sibling of `wi land status`
// and the third HEAL-5 leaf (DESIGN §7.2) — it undoes a parked OR completed land by rewinding
// each landed repo's base to its pre-land anchor. The land/landstate/git guards already prove
// the rewind safety (exact-match CAS, never reset --hard, stale-refusal); the handler's OWN
// responsibilities, untested there, are: (1) take exactly one safe <task> positional (traversal
// rejected at the factory as usage); (2) learn the parked land's repos by pre-loading the record,
// then resolve each base from the manifest into land.RepoSpec (an undeclared repo → not_found, a
// missing manifest → not_found+`wi init`); (3) the AbortResult.Status→envelope/return three-way
// map, mirroring landCmd's (it NEVER assembles an envelope or picks an exit code):
//   - AbortStatusAborted (every landed repo rewound, record discarded) → an action=removed Result
//     (exit 0), each repo action=removed carrying the restored (pre-land) sha;
//   - AbortStatusBlocked with ≥1 repo rewound (a base advanced past its landed tip, so abort
//     refused THAT repo but rewound the rest and KEPT the record) → a DURABLE PARTIAL (result,
//     *CommandError{partial, Action: removed}) — the rewinds are real progress, exit 2;
//   - AbortStatusBlocked with NOTHING rewound → a full refusal *CommandError{conflict} (exit 4);
//   - ErrNoRecord (never landed / already aborted) → not_found; a held lock → lock_held (exit 6).
// A stale-refused repo rides in repos[] as a per-repo conflict coded base_advanced.
//
// Non-vacuity mutant (registered): in landAbortCmd.Run, on the blocked-with-rewinds outcome return
// (result, nil) instead of (result, *CommandError{partial}) → a partial abort mis-reported as a
// clean success → TestLandAbortBlockedKeepsRecordPartial RED (want *cli.CommandError{partial}, got
// nil) while the clean-abort + all-blocked tests stay GREEN (two-sided). Alternate: map the
// all-blocked (nothing rewound) outcome to KindPartial instead of KindConflict →
// TestLandAbortAllBlockedIsConflict RED (a nothing-changed refusal must be exit 4, not 2) while the
// partial test stays GREEN. Both reverted to byte-identical (cached) GREEN.

func landAbortFactory(t *testing.T, l layout.Layout, g *git.Git) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l, Git: g})["land abort"]
	if !ok {
		t.Fatal("BuildRegistry has no \"land abort\" factory")
	}
	return f
}

// runLand drives the real `wi land <task> <repos…>` to produce a parked record + advanced
// bases — the landed state an abort undoes.
func runLand(t *testing.T, l layout.Layout, g *git.Git, ctx context.Context, task string, repos ...string) {
	t.Helper()
	args := append([]string{task}, repos...)
	cmd, err := landFactory(t, l, g)(args)
	if err != nil {
		t.Fatalf("land factory: %v", err)
	}
	if _, err := cmd.Run(cli.WithOpID(ctx, "op_land_setup")); err != nil {
		t.Fatalf("land setup run: %v", err)
	}
}

// advancePastLandedTip fast-forwards repo's SSOT base to a DESCENDANT of its current tip (via a
// throwaway worktree off the base), simulating newer work landed on top after the land. Abort
// must now refuse to rewind repo — its base no longer matches the landed tip (exact-match CAS),
// and rewinding would discard this work.
func advancePastLandedTip(t *testing.T, env *testenv.Env, l layout.Layout, g *git.Git, ctx context.Context, repo string) {
	t.Helper()
	ssot, err := l.Repo(repo)
	if err != nil {
		t.Fatalf("layout.Repo(%s): %v", repo, err)
	}
	other := env.Root + "/ahead-" + repo
	if err := g.AddWorktree(ctx, ssot, other, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree(ahead %s): %v", repo, err)
	}
	env.Git(t, other, "commit", "--allow-empty", "-m", "newer work landed on top of "+repo)
	ahead := env.Git(t, other, "rev-parse", "HEAD")
	env.Git(t, ssot, "update-ref", "refs/heads/"+testenv.DefaultBranch, ahead)
}

// Happy path: api+web landed, then aborted — both bases rewind to their pre-land tip, each repo
// reports action=removed carrying that restored sha, the record is discarded, exit 0.
func TestLandAbortRewindsAndReportsRemoved(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "api", "web")
	materializeIsolate(t, l, g, ctx, "feat", "api", "web")
	landWork(t, env, l, "feat", "api")
	landWork(t, env, l, "feat", "web")

	// Pre-land base tip = the backup anchor abort must restore each base to.
	preLand := map[string]string{}
	for _, repo := range []string{"api", "web"} {
		ssot, _ := l.Repo(repo)
		preLand[repo] = env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
	}
	runLand(t, l, g, ctx, "feat", "api", "web")

	cmd, err := landAbortFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_abort_cli"))
	if err != nil {
		t.Fatalf("Run (clean abort): unexpected error %v", err)
	}
	if res.Action != contract.ActionRemoved {
		t.Errorf("Action = %q, want %q (a full abort removes the land)", res.Action, contract.ActionRemoved)
	}
	for _, repo := range []string{"api", "web"} {
		rr := landOutcome(t, res, repo)
		if rr.Action != contract.ActionRemoved || rr.Error != nil {
			t.Errorf("repo %s outcome = %+v, want removed/no-error", repo, rr)
		}
		ssot, _ := l.Repo(repo)
		if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != preLand[repo] {
			t.Errorf("%s base after abort = %q, want rewound to pre-land tip %q", repo, got, preLand[repo])
		}
		if rr.SHA != preLand[repo] {
			t.Errorf("%s restored sha = %q, want pre-land tip %q", repo, rr.SHA, preLand[repo])
		}
	}
	// The record is discarded — an aborted land is gone (decision #ABORT-DISPOSE).
	if _, err := landstate.Load(l.LandDir(), "feat"); !errors.Is(err, landstate.ErrNoRecord) {
		t.Errorf("record after full abort: want ErrNoRecord, got %v", err)
	}
}

// Durable partial: api+web landed; web's base then advances past its landed tip. Abort rewinds
// api (real progress) but refuses web (stale) → AbortStatusBlocked with a rewind → a partial
// (exit 2), result kept, the record retained, web riding in repos[] as a conflict.
func TestLandAbortBlockedKeepsRecordPartial(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "api", "web")
	materializeIsolate(t, l, g, ctx, "feat", "api", "web")
	landWork(t, env, l, "feat", "api")
	landWork(t, env, l, "feat", "web")

	apiSSOT, _ := l.Repo("api")
	apiPreLand := env.Git(t, apiSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
	runLand(t, l, g, ctx, "feat", "api", "web")
	advancePastLandedTip(t, env, l, g, ctx, "web") // web can no longer be rewound

	webSSOT, _ := l.Repo("web")
	webAdvanced := env.Git(t, webSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch)

	cmd, err := landAbortFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_abort_cli"))

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("partial abort: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindPartial {
		t.Errorf("Kind = %q, want %q (api rewound = durable progress)", ce.Kind, contract.KindPartial)
	}
	if ce.Action != contract.ActionRemoved {
		t.Errorf("partial CommandError.Action = %q, want %q (the verb in flight)", ce.Action, contract.ActionRemoved)
	}
	if res == nil {
		t.Fatal("a durable partial MUST carry a result alongside the error")
	}

	// api rewound cleanly.
	api := landOutcome(t, res, "api")
	if api.Action != contract.ActionRemoved || api.Error != nil {
		t.Errorf("api outcome = %+v, want removed/no-error", api)
	}
	if got := env.Git(t, apiSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != apiPreLand {
		t.Errorf("api base = %q, want rewound to %q", got, apiPreLand)
	}
	// web refused — its base UNTOUCHED, carrying a conflict coded base_advanced.
	web := landOutcome(t, res, "web")
	if web.Error == nil {
		t.Fatalf("web (stale) must carry a per-repo error, got %+v", web)
	}
	if web.Error.Kind != contract.KindConflict {
		t.Errorf("web error kind = %q, want %q", web.Error.Kind, contract.KindConflict)
	}
	if web.Error.Code != "base_advanced" {
		t.Errorf("web error code = %q, want base_advanced", web.Error.Code)
	}
	if got := env.Git(t, webSSOT, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != webAdvanced {
		t.Errorf("web base = %q, want it UNTOUCHED at %q (abort must not clobber newer work)", got, webAdvanced)
	}
	// The record is KEPT so a retry can finish — web still landed.
	rec, err := landstate.Load(l.LandDir(), "feat")
	if err != nil {
		t.Fatalf("record after partial abort: want it kept, got %v", err)
	}
	if c := findRecCell(t, rec, "web"); c.Phase != landstate.PhaseLanded {
		t.Errorf("durable web phase after refused abort = %q, want still landed", c.Phase)
	}
}

// Nothing rewound: the sole landed repo's base advanced past its landed tip, so abort refuses it
// and NOTHING is undone → a full conflict refusal (exit 4), NOT partial. The blocked repo still
// rides in repos[].
func TestLandAbortAllBlockedIsConflict(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "web")
	materializeIsolate(t, l, g, ctx, "feat", "web")
	landWork(t, env, l, "feat", "web")
	runLand(t, l, g, ctx, "feat", "web")
	advancePastLandedTip(t, env, l, g, ctx, "web")

	cmd, err := landAbortFactory(t, l, g)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_abort_cli"))

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("all-blocked: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindConflict {
		t.Errorf("Kind = %q, want %q (nothing rewound is a full refusal)", ce.Kind, contract.KindConflict)
	}
	if res == nil || landOutcome(t, res, "web").Error == nil {
		t.Errorf("the blocked repo must still ride in repos[], got res=%+v", res)
	}
}

// A task with no parked land (never landed, or already aborted) is a clean not_found refusal,
// not an internal error — the record load fails with ErrNoRecord before any mutation.
func TestLandAbortNoRecordIsNotFound(t *testing.T) {
	l := bootstrappedLayout(t)
	cmd, err := landAbortFactory(t, l, git.New(gitexec.New()))([]string{"ghost"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(context.Background(), "op_abort_cli"))
	if res != nil {
		t.Errorf("a task with no parked land must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindNotFound {
		t.Errorf("Kind = %q, want %q (no parked land is not_found, not internal)", ce.Kind, contract.KindNotFound)
	}
	if !contains(ce.Message, "ghost") {
		t.Errorf("not_found message should name the task, got %q", ce.Message)
	}
}

func TestLandAbortFactoryValidatesArgs(t *testing.T) {
	l := bootstrappedLayout(t)
	f := landAbortFactory(t, l, git.New(gitexec.New()))

	for _, args := range [][]string{nil, {}, {"a", "b"}} {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("args %v: want kind=usage, got %v", args, err)
		}
	}
	if _, err := f([]string{"../evil"}); !isUsage(err) {
		t.Errorf("traversing task name: want kind=usage, got %v", err)
	}
	cmd, err := f([]string{"feat"})
	if err != nil || cmd == nil {
		t.Errorf("one safe task arg must build a Command, got cmd=%v err=%v", cmd, err)
	}
}

func findRecCell(t *testing.T, rec landstate.TaskLand, repo string) landstate.RepoLand {
	t.Helper()
	for _, rl := range rec.Repos {
		if rl.Repo == repo {
			return rl
		}
	}
	t.Fatalf("repo %q absent from record", repo)
	return landstate.RepoLand{}
}
