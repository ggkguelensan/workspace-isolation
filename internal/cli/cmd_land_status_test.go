package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// Guard CMD-LAND-STATUS: `wi land status <task>` is the read-only resume-inspection verb
// of HEAL-5 (DESIGN §7.2) — the first, smallest leaf of the land continue/abort/status
// family, and the one that establishes the 2-token `land <verb> <task>` routing the
// mutating verbs reuse. Its OWN responsibilities, none of which the landstate guards
// cover, are: (1) take exactly one safe <task> positional (traversal rejected at the
// factory as usage); (2) load the durable parked-land record via landstate.Load and
// project EACH repo's phase onto repos[] as a read with stage = the landstate phase, a
// landed repo carrying its backup sha (the abort anchor) as SHA; (3) map ErrNoRecord —
// "never landed, or the land already finished and discarded its record" — to a clean
// not_found refusal, NOT an internal error. It is a PURE local read: no git, no network,
// no lock (landstate.Store renames atomically, so a lockless reader never tears).
//
// Non-vacuity mutant (registered): in projectLandStatus, hardcode Stage to a constant
// (e.g. string(landstate.PhaseLanded)) instead of string(rl.Phase) → the blocked/pending
// repos report the wrong stage → TestLandStatusReportsParkedPhases RED (two-sided: the
// not_found + factory tests stay GREEN). Alternate: in the ErrNoRecord branch, classify
// the refusal KindInternal instead of KindNotFound → a missing record surfaces as a wi
// bug, not a clean not_found → TestLandStatusNoRecordIsNotFound RED (two-sided). Both
// reverted to byte-identical (cached) GREEN.

func landStatusFactory(t *testing.T, l layout.Layout) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l})["land status"]
	if !ok {
		t.Fatal("BuildRegistry has no \"land status\" factory")
	}
	return f
}

func TestLandStatusReportsParkedPhases(t *testing.T) {
	l := bootstrappedLayout(t)

	// A parked partial: api landed (backup anchored), web blocked, db never reached.
	rec := landstate.NewTaskLand("feat", "op_z", []string{"api", "web", "db"})
	rec.Repos[0].Phase = landstate.PhaseLanded
	rec.Repos[0].BackupSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rec.Repos[1].Phase = landstate.PhaseBlocked
	// rec.Repos[2] stays PhasePending
	if err := landstate.Store(l.LandDir(), rec); err != nil {
		t.Fatalf("store parked-land record: %v", err)
	}

	cmd, err := landStatusFactory(t, l)([]string{"feat"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Action != contract.ActionRead {
		t.Errorf("Action = %q, want %q (status is a read)", res.Action, contract.ActionRead)
	}
	if len(res.Repos) != 3 {
		t.Fatalf("want 3 repos projected in declared order, got %d", len(res.Repos))
	}

	// Independent expectation, indexed by declared order.
	wantStage := []string{"landed", "blocked", "pending"}
	wantRepo := []string{"api", "web", "db"}
	for i, rr := range res.Repos {
		if rr.Repo != wantRepo[i] {
			t.Errorf("repos[%d].Repo = %q, want %q", i, rr.Repo, wantRepo[i])
		}
		if rr.Action != contract.ActionRead {
			t.Errorf("repos[%d].Action = %q, want %q (every cell of a status read is a read)", i, rr.Action, contract.ActionRead)
		}
		if rr.Stage != wantStage[i] {
			t.Errorf("repos[%d].Stage = %q, want %q (must echo the landstate phase)", i, rr.Stage, wantStage[i])
		}
	}

	// The landed repo carries its backup sha (the abort anchor); the others do not.
	if got := res.Repos[0].SHA; got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("landed repo SHA = %q, want the backup sha", got)
	}
	if got := res.Repos[1].SHA; got != "" {
		t.Errorf("blocked repo SHA = %q, want empty (no backup earned)", got)
	}
	if got := res.Repos[2].SHA; got != "" {
		t.Errorf("pending repo SHA = %q, want empty (no backup earned)", got)
	}
}

func TestLandStatusNoRecordIsNotFound(t *testing.T) {
	l := bootstrappedLayout(t)

	cmd, err := landStatusFactory(t, l)([]string{"ghost"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(context.Background())
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

func TestLandStatusFactoryValidatesArgs(t *testing.T) {
	l := bootstrappedLayout(t)
	f := landStatusFactory(t, l)

	for _, args := range [][]string{nil, {}, {"a", "b"}} {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("args %v: want kind=usage, got %v", args, err)
		}
	}
	// A traversing task name is rejected at the factory (usage), never reaching landstate.
	if _, err := f([]string{"../evil"}); !isUsage(err) {
		t.Errorf("traversing task name: want kind=usage, got %v", err)
	}
	// Exactly one safe arg → a runnable Command.
	cmd, err := f([]string{"feat"})
	if err != nil || cmd == nil {
		t.Errorf("one safe task arg must build a Command, got cmd=%v err=%v", cmd, err)
	}
}
