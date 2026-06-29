package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard CMD-SYNC: the `wi sync [<repo>…]` handler is the seam where the sync domain core
// (continue-on-fail, decision #S) meets the envelope contract. The handler's OWN
// responsibilities, which the internal/sync guards (SYNC-RUN) do NOT cover, are:
//   (1) resolve WHICH repos to sync from the manifest — no operands means EVERY declared
//       repo, named operands a subset, an undeclared name a not_found refusal, a missing
//       manifest not_found+`wi init`, a malformed manifest usage (decision #H);
//   (2) read each repo's URL from the manifest into the RepoSpec sync dials — isolate new
//       ignores the URL, so sync is the command that proves it is plumbed through;
//   (3) map the domain Status onto the return convention — StatusComplete → a synced
//       Result, StatusPartial → the DURABLE PARTIAL (result, *CommandError{Kind: partial,
//       Action: synced}) carrying per-repo detail (decision #D).
// It NEVER assembles an envelope or picks an exit code.
//
// Non-vacuity mutant (registered): in syncCmd.Run, on StatusPartial return (result, nil)
// instead of (result, *CommandError{Kind: partial}) → a partial sync is mis-reported as a
// clean success (no error, exit 0) → TestSyncHandlerDurablePartial RED (want
// *cli.CommandError{partial}, got nil). Alternate: drop the unknown-repo not_found branch
// in selectRepos → TestSyncHandlerUnknownRepoIsNotFound RED.

// syncRepoDecl is one repo to declare in a sync test manifest, with a REAL url (unlike the
// isolate-new manifest's placeholder) because sync actually dials it.
type syncRepoDecl struct{ name, url string }

// writeSyncManifest writes a wi.config.jsonc declaring decls in order, all inheriting
// DefaultBranch as their base via defaults.base. Order is preserved so the projected
// repos[] order is deterministic (config.Parse and sync.Run both keep declaration order).
func writeSyncManifest(t *testing.T, l layout.Layout, decls ...syncRepoDecl) {
	t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"defaults\": { \"base\": ")
	b.WriteString(strconv.Quote(testenv.DefaultBranch))
	b.WriteString(" },\n  \"repos\": [\n")
	for i, d := range decls {
		b.WriteString("    { \"name\": ")
		b.WriteString(strconv.Quote(d.name))
		b.WriteString(", \"url\": ")
		b.WriteString(strconv.Quote(d.url))
		b.WriteString(" }")
		if i < len(decls)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("  ]\n}\n")
	if err := os.WriteFile(l.Config(), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func syncFactory(t *testing.T, l layout.Layout, g *git.Git) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l, Git: g, Clock: clock.System{}})["sync"]
	if !ok {
		t.Fatal("BuildRegistry has no \"sync\" factory")
	}
	return f
}

// Complete path + manifest expansion + URL plumbing: `wi sync` with NO operands syncs
// every declared repo, reading each URL from the manifest, and projects per-repo the
// advanced base sha, base branch, SSOT mirror path, and a not-stale freshness block.
func TestSyncHandlerSyncsAllDeclaredRepos(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	apiOrigin := env.SeedOrigin(t, "api")
	webOrigin := env.SeedOrigin(t, "web")
	apiTip := env.Git(t, apiOrigin, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
	webTip := env.Git(t, webOrigin, "rev-parse", "refs/heads/"+testenv.DefaultBranch)

	writeSyncManifest(t, l,
		syncRepoDecl{"api", "file://" + apiOrigin},
		syncRepoDecl{"web", "file://" + webOrigin},
	)

	cmd, err := syncFactory(t, l, g)(nil) // no operands → all declared repos
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_sync_all"))
	if err != nil {
		t.Fatalf("Run (complete): unexpected error %v", err)
	}
	if res.Action != contract.ActionSynced {
		t.Errorf("Action = %q, want %q", res.Action, contract.ActionSynced)
	}
	if len(res.Repos) != 2 {
		t.Fatalf("want 2 repos projected, got %d", len(res.Repos))
	}
	wantSHA := map[string]string{"api": apiTip, "web": webTip}
	for _, rr := range res.Repos {
		if rr.Action != contract.ActionSynced {
			t.Errorf("repo %s action = %q, want synced", rr.Repo, rr.Action)
		}
		if rr.Error != nil {
			t.Errorf("repo %s: unexpected per-repo error %+v", rr.Repo, rr.Error)
		}
		if rr.SHA != wantSHA[rr.Repo] {
			t.Errorf("repo %s sha = %s, want origin tip %s", rr.Repo, rr.SHA, wantSHA[rr.Repo])
		}
		if rr.Branch != testenv.DefaultBranch {
			t.Errorf("repo %s branch = %q, want %q", rr.Repo, rr.Branch, testenv.DefaultBranch)
		}
		if rr.Mirror == "" {
			t.Errorf("repo %s: mirror path must be populated", rr.Repo)
		}
		if rr.Freshness == nil || rr.Freshness.Stale {
			t.Errorf("repo %s: want non-nil, not-stale freshness, got %+v", rr.Repo, rr.Freshness)
		}
	}
}

// Durable partial (decision #D): an unreachable repo declared FIRST fails, yet the
// reachable repo after it still syncs (continue-on-fail surfaced through the handler), and
// the handler returns BOTH a Result and a *CommandError{Kind: partial, Action: synced}.
func TestSyncHandlerDurablePartial(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	apiOrigin := env.SeedOrigin(t, "api")
	apiTip := env.Git(t, apiOrigin, "rev-parse", "refs/heads/"+testenv.DefaultBranch)

	writeSyncManifest(t, l,
		syncRepoDecl{"ghost", "file://" + filepath.Join(env.Root, "does-not-exist.git")},
		syncRepoDecl{"api", "file://" + apiOrigin},
	)

	cmd, err := syncFactory(t, l, g)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_sync_partial"))

	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("partial: want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindPartial {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindPartial)
	}
	if ce.Action != contract.ActionSynced {
		t.Errorf("partial CommandError.Action = %q, want %q (the verb in flight)", ce.Action, contract.ActionSynced)
	}
	if res == nil {
		t.Fatal("a durable partial MUST carry a result alongside the error (decision #D)")
	}
	if len(res.Repos) != 2 {
		t.Fatalf("want 2 repos in partial result, got %d", len(res.Repos))
	}
	if res.Repos[0].Repo != "ghost" || res.Repos[0].Error == nil {
		t.Errorf("ghost outcome = %+v, want a per-repo error", res.Repos[0])
	}
	api := res.Repos[1]
	if api.Repo != "api" || api.Error != nil || api.Action != contract.ActionSynced {
		t.Errorf("api outcome = %+v, want synced with no error (continue-on-fail)", api)
	}
	if api.SHA != apiTip {
		t.Errorf("api sha = %s, want origin tip %s", api.SHA, apiTip)
	}
}

// An undeclared operand is a not_found refusal naming the repo — resolved BEFORE any
// network I/O, so a typo never dials.
func TestSyncHandlerUnknownRepoIsNotFound(t *testing.T) {
	env, l, g, ctx := isolateEnv(t)
	apiOrigin := env.SeedOrigin(t, "api")
	writeSyncManifest(t, l, syncRepoDecl{"api", "file://" + apiOrigin})

	cmd, err := syncFactory(t, l, g)([]string{"ghost"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(ctx, "op_sync_unknown"))
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
}

// A missing manifest is not_found with a `wi init` hint (not a malformed-manifest report
// and not internal) — the same two-way split isolate new uses (decision #H).
func TestSyncHandlerMissingManifestIsNotFound(t *testing.T) {
	l := bootstrappedLayout(t) // Bootstrap'd, but no wi.config.jsonc written
	g := git.New(gitexec.New())

	cmd, err := syncFactory(t, l, g)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(context.Background(), "op_sync_nomanifest"))
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
}
