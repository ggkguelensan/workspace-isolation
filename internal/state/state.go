// Package state is the SOLE owner of wi's runtime registry under <root>/.wi/state/
// (DESIGN §map line 168). It records which isolates exist and each isolate's
// per-repo materialization stage, persisted with the single .wi/ atomic writer
// (DESIGN §6.2) so the registry is durable across crashes.
//
// The registry is what makes multi-repo isolate creation a durable partial
// success (DESIGN §6.3): isolate new writes an IsolateRecord with every declared
// repo at StagePending, then calls UpdateRepoStage after each worktree add. A
// crash mid-multi-repo therefore leaves a record reflecting EXACTLY the repos
// completed before the crash — the atomic writer guarantees the interrupted flip
// neither applies nor tears the file.
//
// Like internal/mirror, this package takes no Runner and dials nothing — it is
// pure local persistence. Concurrency is the caller's job: isolate/land hold the
// isolate-state:<task> lock (DESIGN §6.1) around a load-modify-store.
//
// Decision #S (PROGRESS): Stage is a small typed-string state vocabulary owned
// here, NOT a closed contract enum — the envelope's RepoResult.Stage is an
// intentionally free-form string projection, and contract owns only the closed
// *wire* enums. The v0 isolate lifecycle is pending → created; the land phase
// vocabulary (pending|landed|blocked) is a SEPARATE landstate concern in v1 and is
// deliberately not conflated here.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
)

// ErrNoRecord reports that no registry record exists for a task yet. Callers
// distinguish "isolate never created" from a real read error.
var ErrNoRecord = errors.New("state: no isolate record")

// Stage is one repo's materialization stage within an isolate (decision #S). The
// v0 lifecycle is StagePending (declared, worktree not yet added) → StageCreated
// (worktree added and recorded).
type Stage string

const (
	StagePending Stage = "pending"
	StageCreated Stage = "created"
)

// RepoRecord is one repo's cell in an isolate: its wi-internal name and current
// stage. (Branch/worktree/sha detail is added when the isolate package needs it.)
type RepoRecord struct {
	Repo  string `json:"repo"`
	Stage Stage  `json:"stage"`
}

// IsolateRecord is the durable registry entry for one isolate (one task). OpID
// ties the record to the op that created it (DESIGN §6.3); Repos is the declared
// set with per-repo stage.
type IsolateRecord struct {
	Task  string       `json:"task"`
	OpID  string       `json:"op_id"`
	Repos []RepoRecord `json:"repos"`
}

// NewIsolateRecord builds a fresh record for task/opID with every declared repo at
// StagePending — the state isolate new persists before adding any worktree, so the
// "all pending" starting point lives in exactly one place.
func NewIsolateRecord(task, opID string, repos []string) IsolateRecord {
	rec := IsolateRecord{Task: task, OpID: opID, Repos: make([]RepoRecord, 0, len(repos))}
	for _, r := range repos {
		rec.Repos = append(rec.Repos, RepoRecord{Repo: r, Stage: StagePending})
	}
	return rec
}

// recordPath returns the registry file for task within stateDir (layout.StateDir()).
// The task name is validated through layout's single traversal chokepoint because
// it becomes a filename — state owns the flat "<task>.json" naming within the
// layout-provided directory, the same way mirror owns "<repo>.json".
func recordPath(stateDir, task string) (string, error) {
	if err := layout.ValidateSegment("task", task); err != nil {
		return "", err
	}
	return filepath.Join(stateDir, task+".json"), nil
}

// Load reads the registry record for task from stateDir. A task with no record
// yields ErrNoRecord. It is a pure local read (no network).
func Load(stateDir, task string) (IsolateRecord, error) {
	p, err := recordPath(stateDir, task)
	if err != nil {
		return IsolateRecord{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return IsolateRecord{}, ErrNoRecord
		}
		return IsolateRecord{}, fmt.Errorf("state: read record %s: %w", p, err)
	}
	var rec IsolateRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return IsolateRecord{}, fmt.Errorf("state: parse record %s: %w", p, err)
	}
	return rec, nil
}

// List enumerates every isolate registry record under stateDir, in task order
// (os.ReadDir sorts by filename, and the flat "<task>.json" naming makes that task
// order). It is gc's and doctor's window onto "which isolates does wi currently
// consider live?" — the Live signal the workspace gc sweep keys evidence-positive
// reclamation on (DESIGN §7.1: a cell journaled as live is never gc's to collect).
//
// A missing stateDir yields the empty list, not an error: a workspace where no
// isolate has ever been created simply has no live records, the same idempotent
// posture as Delete. A non-record file (anything but "<task>.json") is skipped, but
// a .json file that fails to parse is a HARD error — a torn registry entry is real
// drift wi must surface, never silently drop (the same posture git.ListOwnedRefs
// takes on a malformed ref line). Pure local read (no network); a read-only sweep
// needs no lock.
func List(stateDir string) ([]IsolateRecord, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("state: list records in %s: %w", stateDir, err)
	}
	var out []IsolateRecord
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		task := strings.TrimSuffix(e.Name(), ".json")
		rec, err := Load(stateDir, task)
		if err != nil {
			return nil, fmt.Errorf("state: list records in %s: %w", stateDir, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// Store atomically persists rec under stateDir via the single .wi/ atomic writer
// (DESIGN §6.2). stateDir must already exist (layout.Bootstrap creates it).
func Store(stateDir string, rec IsolateRecord) error {
	p, err := recordPath(stateDir, rec.Task)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal record for %q: %w", rec.Task, err)
	}
	data = append(data, '\n')
	if err := lockfs.WriteFileAtomic(p, data, 0o644); err != nil {
		return fmt.Errorf("state: write record %s: %w", p, err)
	}
	return nil
}

// Delete removes task's registry record. A missing record is a no-op success, so
// teardown stays idempotent under a re-run (DESIGN §7.1: reclamation is replayable).
// isolate.Remove calls this once every repo in an isolate has been reclaimed — the
// isolate no longer exists, so its registry entry must go (a subsequent isolate rm
// then correctly reports ErrNoRecord rather than finding an empty-repos husk). The
// caller holds the isolate-state:<task> lock. It is a pure local delete (no network).
func Delete(stateDir, task string) error {
	p, err := recordPath(stateDir, task)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("state: delete record %s: %w", p, err)
	}
	return nil
}

// UpdateRepoStage sets repo's stage in task's record and atomically re-stores it.
// DESIGN §6.3 calls this after each worktree add, so the write goes through the
// atomic writer: an interrupted flip leaves the prior durable record intact rather
// than tearing it or half-applying. The caller holds the isolate-state:<task> lock,
// so the load-modify-store needs no internal synchronization. A missing task yields
// ErrNoRecord; a repo not in the record is an error.
func UpdateRepoStage(stateDir, task, repo string, stage Stage) error {
	rec, err := Load(stateDir, task)
	if err != nil {
		return err
	}
	found := false
	for i := range rec.Repos {
		if rec.Repos[i].Repo == repo {
			rec.Repos[i].Stage = stage
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("state: repo %q not in isolate %q", repo, task)
	}
	return Store(stateDir, rec)
}
