package state_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/fault"
	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
	"github.com/ggkguelensan/workspace-isolation/internal/state"
)

// internal/state is the SOLE owner of the .wi/state/ runtime registry (DESIGN
// §map line 168). This unit covers the per-isolate record: an IsolateRecord
// (task + op_id + per-repo RepoRecord{repo, stage}) persisted atomically and the
// UpdateRepoStage flip that DESIGN §6.3 calls after each worktree add.
//
// Guards:
//   STATE-PERSIST  — Store/Load round-trip, missing → ErrNoRecord, unsafe task
//                    name rejected, and UpdateRepoStage flips exactly one repo.
//   STATE-DURABLE  — durable partial success (DESIGN §6.3): an UpdateRepoStage
//                    interrupted in the atomic-writer crash window leaves the
//                    PRIOR durable record intact (the completed flip survives, the
//                    interrupted one neither applies nor corrupts).

func newRec() state.IsolateRecord {
	return state.NewIsolateRecord("task-x", "op_abc_def", []string{"api", "web", "db"})
}

func TestRecordRoundTrips(t *testing.T) {
	dir := t.TempDir()
	rec := newRec()
	if err := state.Store(dir, rec); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := state.Load(dir, rec.Task)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, rec) {
		t.Errorf("round-trip mismatch:\n got = %+v\nwant = %+v", got, rec)
	}
	// Fresh records start every repo pending.
	for _, r := range got.Repos {
		if r.Stage != state.StagePending {
			t.Errorf("repo %q: fresh stage = %q, want %q", r.Repo, r.Stage, state.StagePending)
		}
	}
}

func TestLoadMissingIsErrNoRecord(t *testing.T) {
	if _, err := state.Load(t.TempDir(), "absent"); !errors.Is(err, state.ErrNoRecord) {
		t.Errorf("Load(absent): want ErrNoRecord, got %v", err)
	}
}

func TestStoreRejectsUnsafeTaskName(t *testing.T) {
	rec := newRec()
	rec.Task = "../escape"
	if err := state.Store(t.TempDir(), rec); err == nil {
		t.Error("Store(unsafe task name): want error, got nil")
	}
}

func TestUpdateRepoStageFlipsOneRepo(t *testing.T) {
	dir := t.TempDir()
	if err := state.Store(dir, newRec()); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := state.UpdateRepoStage(dir, "task-x", "web", state.StageCreated); err != nil {
		t.Fatalf("UpdateRepoStage: %v", err)
	}
	got, err := state.Load(dir, "task-x")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, r := range got.Repos {
		want := state.StagePending
		if r.Repo == "web" {
			want = state.StageCreated
		}
		if r.Stage != want {
			t.Errorf("repo %q: stage = %q, want %q", r.Repo, r.Stage, want)
		}
	}

	// Flipping a repo not in the record is an error (the caller passed a repo the
	// isolate never declared).
	if err := state.UpdateRepoStage(dir, "task-x", "ghost", state.StageCreated); err == nil {
		t.Error("UpdateRepoStage(unknown repo): want error, got nil")
	}
}

func TestDurablePartialSuccess(t *testing.T) {
	dir := t.TempDir()
	if err := state.Store(dir, newRec()); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// First flip lands durably.
	if err := state.UpdateRepoStage(dir, "task-x", "api", state.StageCreated); err != nil {
		t.Fatalf("UpdateRepoStage(api): %v", err)
	}

	// The second flip is interrupted in the atomic-writer's pre-rename crash
	// window (DESIGN §6.2 fault seam). It must fail...
	t.Setenv(fault.EnvVar, lockfs.FaultBeforeRename)
	if err := state.UpdateRepoStage(dir, "task-x", "web", state.StageCreated); err == nil {
		t.Fatal("UpdateRepoStage(web) under injected crash: want error, got nil")
	}

	// ...and the durable registry must reflect EXACTLY the completed flip: api
	// created, web/db still pending, file neither torn nor partially applied.
	got, err := state.Load(dir, "task-x")
	if err != nil {
		t.Fatalf("Load after crash: %v (the record must not be torn)", err)
	}
	want := map[string]state.Stage{"api": state.StageCreated, "web": state.StagePending, "db": state.StagePending}
	for _, r := range got.Repos {
		if r.Stage != want[r.Repo] {
			t.Errorf("repo %q: durable stage = %q, want %q", r.Repo, r.Stage, want[r.Repo])
		}
	}
}

// guard against an accidental path-scheme change: the record file lives flat
// under stateDir as <task>.json (mirrors mirror's <repo>.json), so a sibling
// tool/test can locate it deterministically.
func TestRecordPathIsFlatJSON(t *testing.T) {
	dir := t.TempDir()
	if err := state.Store(dir, newRec()); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := state.Load(filepath.Dir(filepath.Join(dir, "task-x.json")), "task-x"); err != nil {
		t.Errorf("expected record at <stateDir>/task-x.json: %v", err)
	}
}
