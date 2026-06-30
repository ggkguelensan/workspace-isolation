// Package landstate is the SOLE owner of wi's durable parked-land accounting under
// <root>/.wi/land/<task>.json (DESIGN §map line 162, §7.2). It records, per landing
// task, each repo's land phase (pending|landed|blocked) and the backup ref sha
// captured BEFORE any pointer move — the durable state `land continue`/`land abort`/
// `land status` (HEAL-5) resume from after a land parks or crashes.
//
// The record is what makes a multi-repo land a durable, resumable partial success
// (DESIGN §7.2, mirroring how internal/state makes isolate creation one): land writes
// the backup sha and flips a repo to PhaseLanded only AFTER its base ref advances, so
// a crash mid-land leaves a record reflecting EXACTLY the repos already landed — the
// atomic writer guarantees the interrupted flip neither applies nor tears the file.
// `land abort` restores each landed repo from its BackupSHA (never `git reset --hard`,
// DESIGN §7.2). Offline push reconciliation NEVER flips a phase to PhaseLanded.
//
// Like internal/state and internal/mirror, this package takes no Runner and dials
// nothing — it is pure local persistence. Concurrency is the caller's job: land holds
// the isolate-state:<task> lock (DESIGN §6.1) around a load-modify-store.
//
// Decision #S precedent (PROGRESS): Phase is a small typed-string state vocabulary
// owned HERE, NOT a closed contract wire enum — contract owns only the closed *wire*
// enums + the Envelope, while the .wi/ phase accounting is an implementation detail
// the envelope projects from. state.go explicitly defers pending|landed|blocked to
// this package ("a SEPARATE landstate concern in v1").
package landstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
)

// ErrNoRecord reports that no parked-land record exists for a task yet. Callers
// distinguish "task was never landed / land already finished" from a real read error.
var ErrNoRecord = errors.New("landstate: no parked-land record")

// Phase is one repo's land phase within a task (DESIGN §7.2, decision #S). The
// lifecycle is PhasePending (declared, base ref not yet advanced) → PhaseLanded (base
// ref advanced, backup retained for abort) or PhaseBlocked (landing refused — e.g.
// mirror-stale or a rebase conflict — and the op parked for `land continue`).
type Phase string

const (
	PhasePending Phase = "pending"
	PhaseLanded  Phase = "landed"
	PhaseBlocked Phase = "blocked"
)

// RepoLand is one repo's cell in a parked land: its wi-internal name, current phase,
// the backup ref sha captured BEFORE any pointer move, and the landed tip the base was
// advanced to (DESIGN §7.2). BackupSHA is the sha `land abort` restores the base to
// (rather than reset --hard); LandedSHA is the sha the base was fast-forwarded to on a
// land — the value `land abort` asserts the base is STILL at before rewinding (the
// exact-match guard of git.RestoreBaseRef), so abort refuses to clobber work
// fast-forwarded past the landed tip since. Both are empty until earned (a still-pending
// or blocked repo never advanced its base, so it carries no LandedSHA), and json:omitempty
// keeps each off the wire until then.
type RepoLand struct {
	Repo      string `json:"repo"`
	Phase     Phase  `json:"phase"`
	BackupSHA string `json:"backup_sha,omitempty"`
	LandedSHA string `json:"landed_sha,omitempty"`
}

// TaskLand is the durable parked-land record for one task. OpID ties it to the land op
// (DESIGN §6.3); Repos is the per-repo phase accounting `land status` reports and
// `land continue`/`land abort` act on.
type TaskLand struct {
	Task  string     `json:"task"`
	OpID  string     `json:"op_id"`
	Repos []RepoLand `json:"repos"`
}

// NewTaskLand builds a fresh parked-land record for task/opID with every repo at
// PhasePending and no backup yet — the state land persists before moving any ref, so
// the "all pending" starting point lives in exactly one place (mirror of
// state.NewIsolateRecord).
func NewTaskLand(task, opID string, repos []string) TaskLand {
	rec := TaskLand{Task: task, OpID: opID, Repos: make([]RepoLand, 0, len(repos))}
	for _, r := range repos {
		rec.Repos = append(rec.Repos, RepoLand{Repo: r, Phase: PhasePending})
	}
	return rec
}

// recordPath returns the parked-land file for task within landDir (layout.LandDir()).
// The task name is validated through layout's single traversal chokepoint because it
// becomes a filename — landstate owns the flat "<task>.json" naming within the
// layout-provided directory, the same way state owns it within .wi/state.
func recordPath(landDir, task string) (string, error) {
	if err := layout.ValidateSegment("task", task); err != nil {
		return "", err
	}
	return filepath.Join(landDir, task+".json"), nil
}

// Load reads the parked-land record for task from landDir. A task with no parked land
// yields ErrNoRecord. It is a pure local read (no network).
func Load(landDir, task string) (TaskLand, error) {
	p, err := recordPath(landDir, task)
	if err != nil {
		return TaskLand{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return TaskLand{}, ErrNoRecord
		}
		return TaskLand{}, fmt.Errorf("landstate: read record %s: %w", p, err)
	}
	var rec TaskLand
	if err := json.Unmarshal(data, &rec); err != nil {
		return TaskLand{}, fmt.Errorf("landstate: parse record %s: %w", p, err)
	}
	return rec, nil
}

// Store atomically persists rec under landDir via the single .wi/ atomic writer
// (DESIGN §6.2). landDir must already exist (layout.Bootstrap creates .wi/land).
func Store(landDir string, rec TaskLand) error {
	p, err := recordPath(landDir, rec.Task)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("landstate: marshal record for %q: %w", rec.Task, err)
	}
	data = append(data, '\n')
	if err := lockfs.WriteFileAtomic(p, data, 0o644); err != nil {
		return fmt.Errorf("landstate: write record %s: %w", p, err)
	}
	return nil
}
