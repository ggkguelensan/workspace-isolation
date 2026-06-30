package landstate_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
)

// internal/landstate is the SOLE owner of the .wi/land/<task>.json durable
// parked-land accounting (DESIGN §map line 162, §7.2) — the per-repo land phase
// (pending|landed|blocked) plus the backup ref sha captured BEFORE any pointer move,
// the state `land continue`/`land abort`/`land status` (HEAL-5) resume from. This
// keystone unit covers the record + its codec, mirroring internal/state.
//
// Guards:
//   LANDSTATE-PERSIST — Store/Load round-trip, fresh = all pending, missing →
//                       ErrNoRecord, unsafe task name rejected (never written).
//   LANDSTATE-WIRE    — the DURABLE on-disk wire is stable: the concrete JSON keys
//                       (task/op_id/repo/phase/backup_sha/landed_sha) and phase values a
//                       land written by one wi build, recovered by another, must agree
//                       on. Asserting the literal bytes (not just a round-trip) is what
//                       kills a json-tag / phase-constant rename — a round-trip alone
//                       is VACUOUS because Marshal+Unmarshal share the tag (the
//                       LOCK-HOLDER lesson, PROGRESS).
//
// Non-vacuity mutant (registered): rename the `backup_sha` (or `landed_sha`) json tag
// (→ `backupSha`) OR change PhaseBlocked's value ("blocked"→"blocked-MUT").
// TestTaskLandRoundTrips STAYS GREEN (Marshal/Unmarshal stay symmetric), but
// TestStoredWireIsStable RED (the literal "backup_sha"/"landed_sha"/"blocked" is absent
// from the bytes) — pinning the durable wire, surgically, against the rename a round-trip
// cannot see.

func newRec() landstate.TaskLand {
	// A mixed record exercising all three phases: a still-pending repo (no shas), a
	// blocked repo carrying its pre-move backup sha, and a landed repo carrying BOTH its
	// backup anchor and the landed tip the base advanced to — the shape `land status`
	// reports mid-park and `land abort` rewinds from.
	rec := landstate.NewTaskLand("task-x", "op_abc_def", []string{"api", "web", "db"})
	rec.Repos[1].Phase = landstate.PhaseBlocked
	rec.Repos[1].BackupSHA = "0123456789abcdef0123456789abcdef01234567"
	rec.Repos[2].Phase = landstate.PhaseLanded
	rec.Repos[2].BackupSHA = "89abcdef0123456789abcdef0123456789abcdef"
	rec.Repos[2].LandedSHA = "fedcba9876543210fedcba9876543210fedcba98"
	return rec
}

func TestTaskLandRoundTrips(t *testing.T) {
	dir := t.TempDir()
	rec := newRec()
	if err := landstate.Store(dir, rec); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := landstate.Load(dir, rec.Task)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, rec) {
		t.Errorf("round-trip mismatch:\n got = %+v\nwant = %+v", got, rec)
	}
}

func TestNewTaskLandStartsAllPending(t *testing.T) {
	rec := landstate.NewTaskLand("t", "op_1", []string{"api", "web", "db"})
	if len(rec.Repos) != 3 {
		t.Fatalf("len(Repos) = %d, want 3", len(rec.Repos))
	}
	for _, r := range rec.Repos {
		if r.Phase != landstate.PhasePending {
			t.Errorf("repo %q: fresh phase = %q, want %q", r.Repo, r.Phase, landstate.PhasePending)
		}
		if r.BackupSHA != "" {
			t.Errorf("repo %q: fresh BackupSHA = %q, want empty (no backup until a ref moves)", r.Repo, r.BackupSHA)
		}
	}
}

func TestLoadMissingIsErrNoRecord(t *testing.T) {
	if _, err := landstate.Load(t.TempDir(), "absent"); !errors.Is(err, landstate.ErrNoRecord) {
		t.Errorf("Load(absent): want ErrNoRecord, got %v", err)
	}
}

func TestStoreRejectsUnsafeTaskName(t *testing.T) {
	dir := t.TempDir()
	rec := newRec()
	rec.Task = "../evil"
	if err := landstate.Store(dir, rec); err == nil {
		t.Errorf("Store(unsafe task): want error, got nil")
	}
	// The traversal must not have escaped the land dir.
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("Store(unsafe task) wrote %d files, want 0", len(entries))
	}
}

func TestStoredWireIsStable(t *testing.T) {
	dir := t.TempDir()
	rec := newRec()
	if err := landstate.Store(dir, rec); err != nil {
		t.Fatalf("Store: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "task-x.json"))
	if err != nil {
		t.Fatalf("read stored record: %v", err)
	}
	got := string(data)
	// The closed durable wire: every key another wi build reads back by, plus the
	// concrete blocked-phase value, the backup sha captured before the pointer move, and
	// the landed tip a landed repo records (the abort exact-match anchor).
	for _, want := range []string{
		`"task"`,
		`"op_id"`,
		`"repo"`,
		`"phase"`,
		`"backup_sha"`,
		`"landed_sha"`,
		`"blocked"`,
		`"landed"`,
		"0123456789abcdef0123456789abcdef01234567",
		"fedcba9876543210fedcba9876543210fedcba98",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stored record missing durable wire token %s\nin: %s", want, got)
		}
	}
}
