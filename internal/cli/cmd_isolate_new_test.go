package cli_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard CMD-ISOLATE-NEW: the `wi isolate new <task> <repo>…` handler is the marquee
// command — the seam where the isolate domain core (durable partial success) meets the
// envelope contract. The handler's OWN responsibilities, which the isolate-package guards
// do NOT cover, are: (1) resolve each requested repo name against the manifest into a
// RepoSpec, refusing an undeclared repo with not_found and a missing manifest with
// not_found+`wi init` hint; (2) record the SAME op_id the pipeline minted by reading it
// from the context (OpIDFrom) — proving the CTX-OPID seam pays off in durable state; and
// (3) map the domain Status onto the return convention — StatusComplete → a created
// Result, StatusPartial → the DURABLE PARTIAL (result, *CommandError{Kind: partial})
// carrying per-repo detail (decision #D), a held lock → kind=lock_held. It NEVER builds an
// envelope or picks an exit code.
//
// Non-vacuity mutant (registered): in isolateNewCmd.Run, on StatusPartial return
// `(result, nil)` instead of `(result, *CommandError{Kind: partial})` → a partial is
// mis-reported as a clean success (no error, exit 0) → TestIsolateNewDurablePartial RED
// (want *cli.CommandError{partial}, got nil). Alternate: drop the unknown-repo `!ok`
// not_found branch → TestIsolateNewUnknownRepoIsNotFound RED (wrong kind / no error).

// isolateEnv returns a Bootstrap'd layout over a hermetic git env, plus a Git and ctx —
// the precondition isolate.New (and thus the handler) needs to add worktrees.
func isolateEnv(t *testing.T) (*testenv.Env, layout.Layout, *git.Git, context.Context) {
	t.Helper()
	env := testenv.New(t)
	l, err := layout.Resolve(env.Root)
	if err != nil {
		t.Fatalf("layout.Resolve: %v", err)
	}
	if err := l.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return env, l, git.New(gitexec.NewWithEnv("git", env.GitEnv())), context.Background()
}

// seedSSOT seeds a hermetic origin for name and materializes its SSOT clone at
// repos/<name> — the precondition for materializing a worktree off it.
func seedSSOT(t *testing.T, env *testenv.Env, l layout.Layout, g *git.Git, ctx context.Context, name string) {
	t.Helper()
	origin := env.SeedOrigin(t, name)
	ssot, err := l.Repo(name)
	if err != nil {
		t.Fatalf("layout.Repo(%s): %v", name, err)
	}
	if err := g.EnsureClone(ctx, ssot, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone(%s): %v", name, err)
	}
}

// writeManifest writes a wi.config.jsonc declaring repos, all inheriting DefaultBranch as
// their base via defaults.base. The url is a placeholder — isolate new works off the
// already-cloned SSOT and never dials it (only sync/EnsureClone use the url).
func writeManifest(t *testing.T, l layout.Layout, repos ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"defaults\": { \"base\": ")
	b.WriteString(strconv.Quote(testenv.DefaultBranch))
	b.WriteString(" },\n  \"repos\": [\n")
	for i, r := range repos {
		b.WriteString("    { \"name\": ")
		b.WriteString(strconv.Quote(r))
		b.WriteString(", \"url\": \"file:///unused\" }")
		if i < len(repos)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("  ]\n}\n")
	if err := os.WriteFile(l.Config(), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func isolateNewFactory(t *testing.T, l layout.Layout, g *git.Git) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l, Git: g})["isolate new"]
	if !ok {
		t.Fatal("BuildRegistry has no \"isolate new\" factory")
	}
	return f
}

// Complete path + CTX-OPID payoff: every declared repo materializes, the Result is a
// created success carrying per-repo detail, and the durable record's op_id is EXACTLY the
// one injected into the context (not a freshly-minted divergent id).
func TestIsolateNewCreatesWorktreesAndRecordsOpID(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	seedSSOT(t, env, l, g, ctx, "web")
	writeManifest(t, l, "api", "web")

	const opID = "op_isolate_new_complete"
	ctx = cli.WithOpID(ctx, opID)

	cmd, err := isolateNewFactory(t, l, g)([]string{"feat", "api", "web"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(ctx)
	if err != nil {
		t.Fatalf("Run (complete): unexpected error %v", err)
	}
	if res.Action != contract.ActionCreated {
		t.Errorf("Action = %q, want %q", res.Action, contract.ActionCreated)
	}
	if len(res.Repos) != 2 {
		t.Fatalf("want 2 repos projected, got %d", len(res.Repos))
	}
	for i, want := range []string{"api", "web"} {
		rr := res.Repos[i]
		if rr.Repo != want || rr.Action != contract.ActionCreated || rr.Stage != string(state.StageCreated) {
			t.Errorf("repo[%d] = %+v, want %s/created", i, rr, want)
		}
		if rr.Worktree == "" || rr.SHA == "" {
			t.Errorf("repo %s: worktree/sha must be populated, got worktree=%q sha=%q", want, rr.Worktree, rr.SHA)
		}
		if rr.Error != nil {
			t.Errorf("repo %s: unexpected per-repo error %+v", want, rr.Error)
		}
	}

	// CTX-OPID payoff: the handler recorded the op_id it read from the context.
	rec, err := state.Load(l.StateDir(), "feat")
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if rec.OpID != opID {
		t.Errorf("durable record op_id = %q, want %q (handler must use OpIDFrom(ctx))", rec.OpID, opID)
	}
}

// Durable partial (decision #D): web has no SSOT, so its worktree add fails; api (before
// it) stays created. The handler maps StatusPartial onto BOTH a Result (carrying per-repo
// detail) AND a *CommandError{Kind: partial} — the only case both returns are non-nil.
func TestIsolateNewDurablePartial(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api") // exists
	// "web" intentionally has no repos/web SSOT → its worktree add fails.
	writeManifest(t, l, "api", "web")

	cmd, err := isolateNewFactory(t, l, g)([]string{"feat", "api", "web"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_partial"))

	// A durable partial returns BOTH a result and a *CommandError{partial}.
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("partial: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindPartial {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindPartial)
	}
	if ce.Action != contract.ActionCreated {
		t.Errorf("partial CommandError.Action = %q, want %q (the verb in flight)", ce.Action, contract.ActionCreated)
	}
	if res == nil {
		t.Fatal("a durable partial MUST carry a result alongside the error (decision #D)")
	}
	if len(res.Repos) != 2 {
		t.Fatalf("want 2 repos in partial result, got %d", len(res.Repos))
	}
	if res.Repos[0].Repo != "api" || res.Repos[0].Action != contract.ActionCreated {
		t.Errorf("api outcome = %+v, want created", res.Repos[0])
	}
	if res.Repos[1].Repo != "web" || res.Repos[1].Error == nil {
		t.Errorf("web outcome = %+v, want a per-repo error", res.Repos[1])
	}

	// Durable registry reflects exactly the completed set (resumable).
	rec, err := state.Load(l.StateDir(), "feat")
	if err != nil {
		t.Fatalf("state.Load after partial: %v", err)
	}
	want := map[string]state.Stage{"api": state.StageCreated, "web": state.StagePending}
	for _, rr := range rec.Repos {
		if rr.Stage != want[rr.Repo] {
			t.Errorf("durable %s = %q, want %q", rr.Repo, rr.Stage, want[rr.Repo])
		}
	}
}

// An undeclared repo is a not_found refusal naming the repo — resolved BEFORE any
// materialization, so no state record is written (nothing was attempted).
func TestIsolateNewUnknownRepoIsNotFound(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	seedSSOT(t, env, l, g, ctx, "api")
	writeManifest(t, l, "api")

	cmd, err := isolateNewFactory(t, l, g)([]string{"feat", "ghost"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_unknown"))
	if res != nil {
		t.Errorf("an undeclared repo must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindNotFound {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindNotFound)
	}
	if !contains(ce.Message, "ghost") {
		t.Errorf("not_found message should name the repo, got %q", ce.Message)
	}
	if _, err := state.Load(l.StateDir(), "feat"); !errors.Is(err, state.ErrNoRecord) {
		t.Errorf("no record must be written when resolution fails, got %v", err)
	}
}

// A missing manifest is not_found with a `wi init` hint (not a malformed-manifest report
// and not internal) — resolved before any materialization, so no state record.
func TestIsolateNewMissingManifestIsNotFound(t *testing.T) {
	l := bootstrappedLayout(t) // Bootstrap'd, but no wi.config.jsonc written
	g := git.New(gitexec.New())

	cmd, err := isolateNewFactory(t, l, g)([]string{"feat", "api"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(context.Background(), "op_nomanifest"))
	if res != nil {
		t.Errorf("a missing manifest must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindNotFound {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindNotFound)
	}
	if !contains(ce.Help, "wi init") {
		t.Errorf("missing-manifest help should point at `wi init`, got %q", ce.Help)
	}
	if _, err := state.Load(l.StateDir(), "feat"); !errors.Is(err, state.ErrNoRecord) {
		t.Errorf("no record must be written for a missing manifest, got %v", err)
	}
}

func TestIsolateNewFactoryValidatesArgs(t *testing.T) {
	l := bootstrappedLayout(t)
	f := isolateNewFactory(t, l, git.New(gitexec.New()))

	// fewer than <task> + ≥1 <repo> → usage.
	for _, args := range [][]string{nil, {}, {"feat"}} {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("args %v: want kind=usage, got %v", args, err)
		}
	}
	// a traversing task name is rejected at the factory, never reaching the manifest.
	if _, err := f([]string{"../evil", "api"}); !isUsage(err) {
		t.Errorf("traversing task name: want kind=usage, got %v", err)
	}
	// a safe task + ≥1 repo → a runnable Command.
	cmd, err := f([]string{"feat", "api"})
	if err != nil || cmd == nil {
		t.Errorf("safe task + repo must build a Command, got cmd=%v err=%v", cmd, err)
	}
}
