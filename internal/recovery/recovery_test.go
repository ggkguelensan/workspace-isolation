package recovery_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/recovery"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard HEAL-CRASH-RECOVER (dispatcher limb) — the per-kind Finisher the offline
// roll-forward executor (sub-unit 3c) injects (HEAL-4 sub-unit 3d-iii). journal.Recover
// takes an injected journal.Finisher so the journal package stays free of isolate/land
// deps (no import cycle); recovery.Finisher supplies it, routing each rolled-forward op
// by Kind to the domain that completes it. The live routes are: isolate_rm →
// isolate.FinishRemove; land → land.FinishLand (recovery resolves the journaled repos'
// bases from the manifest first, since the landstate record stores shas not base names);
// every OTHER kind (today only isolate_new, plus any unknown) → error (journal LEFT for
// retry, surfaced in the report — never silently discard an op recovery can't complete,
// the conservative posture of HEAL-4).
//
// Non-vacuity (registered as the HEAL-CRASH-RECOVER dispatcher-limb pair):
//   - (primary — real routing) route isolate_rm to the error default (or stub the arm
//     `return nil` without calling FinishRemove) → the interrupted teardown is never
//     finished → TestFinisherFinishesIsolateRm RED: a `return nil` stub leaves the
//     record present (state.Load != ErrNoRecord); routing to the default returns an
//     error (want nil). One end-to-end test kills both mis-routings.
//   - (alternate — conservative default) make the default arm `return nil` → an op of
//     an unsupported kind is reported finished and its journal discarded unfinished →
//     TestFinisherUnsupportedKindErrors RED (want a non-nil error).
//
// The land route is pinned by its own guard HEAL-FINISH-LAND (see
// TestRecoverFinishesCrashedLand / TestRecoverStillBlockedLandRollsForwardNotFailed below).

// bootstrap returns a Bootstrap'd layout over a hermetic env, plus a Git and ctx.
func bootstrap(t *testing.T) (*testenv.Env, layout.Layout, *git.Git, context.Context) {
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

// seedIsolate bootstraps a hermetic workspace, clones the named SSOTs, and materializes
// an isolate "feat" over them — the realistic precondition for a teardown to finish.
func seedIsolate(t *testing.T, names ...string) (layout.Layout, *git.Git, context.Context) {
	t.Helper()
	env, l, g, ctx := bootstrap(t)
	for _, name := range names {
		origin := env.SeedOrigin(t, name)
		ssot, err := l.Repo(name)
		if err != nil {
			t.Fatalf("layout.Repo(%s): %v", name, err)
		}
		if err := g.EnsureClone(ctx, ssot, "file://"+origin, testenv.DefaultBranch); err != nil {
			t.Fatalf("EnsureClone(%s): %v", name, err)
		}
	}
	specs := make([]isolate.RepoSpec, len(names))
	for i, n := range names {
		specs[i] = isolate.RepoSpec{Name: n, Base: testenv.DefaultBranch}
	}
	if _, err := isolate.New(ctx, l, g, "feat", "op_new", specs); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	return l, g, ctx
}

// TestFinisherFinishesIsolateRm pins the load-bearing route: a rolled-forward isolate_rm
// op handed to the dispatcher's Finisher actually finishes the teardown — the isolate
// (record + worktrees) is gone afterward, and the call returns nil so the executor closes
// the lifecycle. This is the end-to-end proof that the dispatcher routes to the real
// isolate.FinishRemove (not a no-op, not the error default).
func TestFinisherFinishesIsolateRm(t *testing.T) {
	l, g, ctx := seedIsolate(t, "api", "web")

	fin := recovery.Finisher(ctx, l, g)
	op := journal.OpRecovery{OpID: "op_rm_recover", Kind: journal.KindIsolateRm, Task: "feat", Repos: nil}
	if err := fin(op); err != nil {
		t.Fatalf("Finisher(isolate_rm) = %v, want nil (teardown must finish)", err)
	}

	if _, err := state.Load(l.StateDir(), "feat"); !errors.Is(err, state.ErrNoRecord) {
		t.Errorf("state.Load after recovery err = %v, want ErrNoRecord (dispatcher must finish the teardown)", err)
	}
}

// TestFinisherUnsupportedKindErrors pins the conservative default: an op of a kind no
// finisher in this build can complete (today isolate_new — it has no roll-forward route
// yet) returns an error, so the executor LEAVES its journal in place and surfaces it —
// recovery never silently discards an op it cannot finish. (land USED to be the example
// here; it now has a finisher — see TestRecoverFinishesCrashedLand — so this uses
// isolate_new, the remaining unsupported kind.)
func TestFinisherUnsupportedKindErrors(t *testing.T) {
	_, l, g, ctx := bootstrap(t) // no isolate needed; the op never touches one

	fin := recovery.Finisher(ctx, l, g)
	op := journal.OpRecovery{OpID: "op_iso_new", Kind: journal.KindIsolateNew, Task: "feat"}
	if err := fin(op); err == nil {
		t.Errorf("Finisher(isolate_new) = nil, want a non-nil error (no finisher for this kind → journal left for retry)")
	}
}

// Guard HEAL-FINISH-LAND — the land roll-forward Finisher (land.FinishLand, the land mirror
// of isolate.FinishRemove), exercised END-TO-END through the recovery dispatcher: recovery
// routes a rolled-forward KindLand op to a Finisher that resolves the journaled repos' bases
// from the manifest and re-runs the non-journaling Continue core to drain a land interrupted
// mid-flight (DESIGN §7.4, HEAL-5). It re-runs Continue (NOT Run) so an already-landed cell's
// backup anchor survives; it journals nothing (the executor owns journal mutation).
//
// THE RULING (registered) — land DIVERGES from isolate.FinishRemove: a residual non-ff land
// block returns nil (the op rolls FORWARD, journal discarded) because a blind re-run cannot
// unblock a non-ff (it needs a rebase, HEAL-6) — the durable landstate record, not the
// journal, is the resume source. isolate.FinishRemove errors on a still-blocked orphan
// because an orphan CAN later resolve on a re-run; a land block cannot.
//
// Non-vacuity mutants (registered as the HEAL-FINISH-LAND pair):
//   - (primary — drain) make land.FinishLand a no-op `return nil` (skip the Continue call) →
//     TestRecoverFinishesCrashedLand RED (the still-pending repo never lands; the base ref is
//     not advanced to the work tip) while TestRecoverStillBlocked… stays GREEN (nil → rolled
//     forward, base legitimately untouched) — proving FinishLand genuinely drains via Continue.
//   - (the ruling) make land.FinishLand capture Continue's Result and `return err` when its
//     Status is StatusBlocked (the isolate.FinishRemove posture) →
//     TestRecoverStillBlockedLandRollsForwardNotFailed RED (the op lands in Failed and its
//     journal is pinned, when the ruling demands RolledForward) while
//     TestRecoverFinishesCrashedLand stays GREEN (a clean land is StatusLanded → nil).

// writeLandManifest writes a minimal wi.config.jsonc declaring each repo with the default
// base — the manifest recovery reads to resolve a journaled land's repo bases (the landstate
// record stores shas, not base branch names, exactly as `wi land continue` resolves them).
func writeLandManifest(t *testing.T, l layout.Layout, repos ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"defaults":{"base":`)
	b.WriteString(strconv.Quote(testenv.DefaultBranch))
	b.WriteString(`},"repos":[`)
	for i, r := range repos {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":`)
		b.WriteString(strconv.Quote(r))
		b.WriteString(`,"url":"file:///unused"}`)
	}
	b.WriteString("]}\n")
	if err := os.WriteFile(l.Config(), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// commitInWorktree writes a file in wt and commits it, returning the new HEAD sha — the work
// tip a land must carry into the base.
func commitInWorktree(t *testing.T, env *testenv.Env, wt, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write work file: %v", err)
	}
	env.Git(t, wt, "add", file)
	env.Git(t, wt, "commit", "-m", "work")
	return env.Git(t, wt, "rev-parse", "HEAD")
}

// seedLandIsolate bootstraps a hermetic workspace, clones repo into its SSOT, writes a
// manifest declaring it, and materializes the isolate "feat" over it — returning the env, the
// layout/git/ctx, the SSOT dir, and the isolate worktree path. The caller commits work in the
// worktree (so the repo can fast-forward) and then stages a crashed land.
func seedLandIsolate(t *testing.T, repo string) (*testenv.Env, layout.Layout, *git.Git, context.Context, string, string) {
	t.Helper()
	env, l, g, ctx := bootstrap(t)
	origin := env.SeedOrigin(t, repo)
	ssot, err := l.Repo(repo)
	if err != nil {
		t.Fatalf("layout.Repo(%s): %v", repo, err)
	}
	if err := g.EnsureClone(ctx, ssot, "file://"+origin, testenv.DefaultBranch); err != nil {
		t.Fatalf("EnsureClone(%s): %v", repo, err)
	}
	writeLandManifest(t, l, repo)
	if _, err := isolate.New(ctx, l, g, "feat", "op_iso", []isolate.RepoSpec{{Name: repo, Base: testenv.DefaultBranch}}); err != nil {
		t.Fatalf("isolate.New: %v", err)
	}
	wt, err := l.Isolate("feat", repo)
	if err != nil {
		t.Fatalf("layout.Isolate: %v", err)
	}
	return env, l, g, ctx, ssot, wt
}

// stageCrashedLand writes the durable artifacts a `wi land` leaves at its commit point but
// before completion: an all-pending landstate record plus a journal stuck at `committed`
// (intent + committed, no done) — exactly the state a process that DIED mid-land leaves for
// the offline executor to roll forward.
func stageCrashedLand(t *testing.T, l layout.Layout, opID, task string, repos ...string) {
	t.Helper()
	if err := landstate.Store(l.LandDir(), landstate.NewTaskLand(task, opID, repos)); err != nil {
		t.Fatalf("stage landstate record: %v", err)
	}
	for _, ph := range []journal.Phase{journal.PhaseIntent, journal.PhaseCommitted} {
		e := journal.Entry{OpID: opID, Kind: journal.KindLand, Phase: ph, Task: task, Repos: repos}
		if err := journal.Append(l.JournalDir(), e); err != nil {
			t.Fatalf("stage journal %s: %v", ph, err)
		}
	}
}

func hasOp(ops []string, op string) bool {
	for _, o := range ops {
		if o == op {
			return true
		}
	}
	return false
}

func findRecCell(t *testing.T, rec landstate.TaskLand, repo string) landstate.RepoLand {
	t.Helper()
	for _, c := range rec.Repos {
		if c.Repo == repo {
			return c
		}
	}
	t.Fatalf("repo %q absent from landstate record", repo)
	return landstate.RepoLand{}
}

// TestRecoverFinishesCrashedLand is the load-bearing land route: a `wi land` that DIED at its
// commit point (journal at `committed`, an all-pending durable record, no `done`) is rolled
// FORWARD by recovery — the dispatcher routes the KindLand op to the land Finisher, which
// resolves the repo's base from the manifest and re-runs the Continue core to drain the
// still-pending repo onto its base. After recovery the base ref has advanced to the work tip,
// the op is RolledForward (not Failed), the journal is cleared, and the durable record reads
// landed.
func TestRecoverFinishesCrashedLand(t *testing.T) {
	env, l, g, ctx, ssot, wt := seedLandIsolate(t, "api")
	workTip := commitInWorktree(t, env, wt, "feature.txt", "work\n")
	baseBefore := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch)
	if workTip == baseBefore {
		t.Fatalf("precondition: work tip must advance past base")
	}
	stageCrashedLand(t, l, "op_land", "feat", "api")

	rep, err := journal.Recover(l.JournalDir(), recovery.Finisher(ctx, l, g))
	if err != nil {
		t.Fatalf("journal.Recover: %v", err)
	}
	if !hasOp(rep.RolledForward, "op_land") {
		t.Errorf("RolledForward = %v, want it to contain op_land (the crashed land must finish)", rep.RolledForward)
	}
	if len(rep.Failed) != 0 {
		t.Errorf("Failed = %v, want empty (the drain must succeed)", rep.Failed)
	}
	// The drain LANDED api: its base ref advanced to the work tip.
	if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != workTip {
		t.Errorf("base ref after recovery = %q, want the work tip %q (recovery must land the pending repo)", got, workTip)
	}
	// The journal self-cleaned, and the durable record reflects the land (so HEAL-5 still sees
	// an abortable completed land, not a phantom).
	if ops, serr := journal.Scan(l.JournalDir()); serr != nil || len(ops) != 0 {
		t.Errorf("journal after roll-forward: %d op(s) (err %v), want 0", len(ops), serr)
	}
	rec, err := landstate.Load(l.LandDir(), "feat")
	if err != nil {
		t.Fatalf("landstate.Load: %v", err)
	}
	if c := findRecCell(t, rec, "api"); c.Phase != landstate.PhaseLanded {
		t.Errorf("durable api Phase = %q, want landed", c.Phase)
	}
}

// TestRecoverStillBlockedLandRollsForwardNotFailed pins THE RULING (the land Finisher's
// documented divergence from isolate.FinishRemove): when a crashed land's repo STILL refuses
// on the re-run (a non-fast-forward a competing land left behind), recovery rolls the op
// FORWARD anyway — the Finisher returns nil, so the op is RolledForward and its journal is
// DISCARDED, NOT left in Failed for a futile retry. A non-ff cannot self-resolve on a blind
// re-run (it needs a rebase, HEAL-6); the block's full state is durable in the landstate
// record, which `land continue`/`land abort` resume from — not the journal.
func TestRecoverStillBlockedLandRollsForwardNotFailed(t *testing.T) {
	env, l, g, ctx, ssot, wt := seedLandIsolate(t, "api")
	commitInWorktree(t, env, wt, "feature.txt", "api work\n")
	// A competing land moved api's base to a DIVERGENT commit after the isolate was created,
	// so the work no longer fast-forwards — the re-attempt will REFUSE.
	other := filepath.Join(env.Root, "other-api")
	if err := g.AddWorktree(ctx, ssot, other, "refs/heads/"+testenv.DefaultBranch); err != nil {
		t.Fatalf("AddWorktree(other): %v", err)
	}
	divergent := commitInWorktree(t, env, other, "other.txt", "competing\n")
	env.Git(t, ssot, "update-ref", "refs/heads/"+testenv.DefaultBranch, divergent)
	stageCrashedLand(t, l, "op_land", "feat", "api")

	rep, err := journal.Recover(l.JournalDir(), recovery.Finisher(ctx, l, g))
	if err != nil {
		t.Fatalf("journal.Recover: %v", err)
	}
	if !hasOp(rep.RolledForward, "op_land") {
		t.Errorf("RolledForward = %v, want op_land (a still-blocked land must roll forward, not be retried forever)", rep.RolledForward)
	}
	if hasOp(rep.Failed, "op_land") {
		t.Errorf("Failed = %v, want op_land ABSENT (a non-ff block is not a recoverable fault — the record, not the journal, is the resume source)", rep.Failed)
	}
	// The journal was discarded (not pinned for retry)...
	if ops, serr := journal.Scan(l.JournalDir()); serr != nil || len(ops) != 0 {
		t.Errorf("journal after refused recovery: %d op(s) (err %v), want 0 — a residual land block must NOT pin a roll-forward retry", len(ops), serr)
	}
	// ...and the base ref is LEFT UNTOUCHED at the divergent tip — recovery never forces a base.
	if got := env.Git(t, ssot, "rev-parse", "refs/heads/"+testenv.DefaultBranch); got != divergent {
		t.Errorf("base ref after refused recovery = %q, want it unchanged at %q", got, divergent)
	}
}
