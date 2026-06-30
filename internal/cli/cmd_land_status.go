package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// newLandStatusCommand is the `wi land status <task>` factory: the read-only verb of the
// HEAL-5 land continue/abort/status family (DESIGN §7.2). It takes exactly one safe
// <task> positional — the traversal check happens HERE so a bad task name is a clean
// usage refusal rather than an opaque landstate error later — and binds task+layout into
// a runnable Command. No git driver is needed: status is a pure landstate read. The
// 2-token "land status" registry key beats the 1-token "land" via Dispatch's longest
// match, so `wi land status feat` routes here while `wi land feat <repo>…` still lands.
func newLandStatusCommand(l layout.Layout, args []string) (Command, error) {
	if len(args) != 1 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi land status <task>"}
	}
	task := args[0]
	if err := layout.ValidateSegment("task", task); err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: err.Error()}
	}
	return &landStatusCmd{layout: l, task: task}, nil
}

// landStatusCmd answers "where did this land park?" as a PURE projection (DESIGN §7.2,
// mirroring resolveCmd): load the durable parked-land record and project each repo's
// phase onto repos[]. No git, no network, and NO lock — landstate.Store renames the file
// atomically, so a lockless reader always sees a whole record, never a torn one. A task
// with no parked land (ErrNoRecord) — never landed, or a land that finished cleanly and
// discarded its record — is a not_found refusal, not an internal error; any other load
// failure stays unclassified → internal (a real bug, surfaced). It never builds an
// envelope or picks an exit code — the pipeline owns that.
type landStatusCmd struct {
	layout layout.Layout
	task   string
}

func (c *landStatusCmd) Run(ctx context.Context) (*Result, error) {
	rec, err := landstate.Load(c.layout.LandDir(), c.task)
	if errors.Is(err, landstate.ErrNoRecord) {
		return nil, &CommandError{
			Kind:    contract.KindNotFound,
			Message: fmt.Sprintf("no parked land for %q", c.task),
			Help:    "it was never landed, or the land already finished; check the task name",
		}
	}
	if err != nil {
		return nil, err
	}

	repos := make([]contract.RepoResult, len(rec.Repos))
	for i, rl := range rec.Repos {
		repos[i] = projectLandStatus(rl)
	}
	return &Result{Action: contract.ActionRead, Repos: repos}, nil
}

// projectLandStatus maps one durable RepoLand cell onto the wire RepoResult for a status
// read. Every cell is action=read (status mutates nothing); stage echoes the landstate
// phase so the agent sees each repo's exact lifecycle position (landed|blocked|pending),
// and a landed repo surfaces its backup sha as SHA — the anchor `land abort` would
// restore it to (DESIGN §7.2). A pending/blocked repo earned no backup yet, so it carries
// no SHA. Mirror/Branch stay empty for v1, matching projectLandOutcome.
func projectLandStatus(rl landstate.RepoLand) contract.RepoResult {
	return contract.RepoResult{
		Repo:   rl.Repo,
		Action: contract.ActionRead,
		SHA:    rl.BackupSHA,
		Stage:  string(rl.Phase),
	}
}
