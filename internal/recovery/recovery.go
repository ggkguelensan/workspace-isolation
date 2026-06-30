// Package recovery wires the journal's offline roll-forward executor to the domains
// that complete each kind of interrupted operation (HEAL-4 sub-unit 3d-iii, DESIGN
// §7.4). journal.Recover takes an INJECTED journal.Finisher so the journal package
// stays free of isolate/land dependencies (no import cycle); this package supplies
// that Finisher, routing each rolled-forward op by Kind to the domain that finishes it.
//
// It sits ABOVE journal and the domain packages precisely so it can depend on both:
// journal defines the Finisher contract, isolate/land implement the per-kind work, and
// recovery is the seam that joins them. The eventual offline startup hook (a later
// unit) calls journal.Recover(l.JournalDir(), recovery.Finisher(ctx, l, g)) before the
// command body, under the workspace lock, dialing no network.
package recovery

import (
	"context"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/baseref"
	"github.com/ggkguelensan/workspace-isolation/internal/config"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/isolate"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/land"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// Finisher builds the journal.Finisher the offline executor injects: it routes each
// rolled-forward op by Kind to the domain that can complete it.
//
//   - isolate_rm → isolate.FinishRemove: finish an interrupted teardown. Idempotent (a
//     teardown that already completed is a no-op success); a still-blocked orphan
//     returns an error so the executor leaves the journal for the next startup to retry.
//   - land → finishLand → land.FinishLand: drain a land interrupted mid-flight by re-running
//     the Continue core. Bases are resolved from the manifest here (the durable record stores
//     shas, not base names) before the offline domain call. A residual non-ff block rolls
//     FORWARD (returns nil) rather than erroring — see land.FinishLand's RULING.
//
// Any other kind has no finisher in this build — today isolate_new does not journal a
// recoverable lifecycle, so a rolled-forward op of another kind is either a future feature or
// a corrupt entry. Either way it returns an error so the executor LEAVES the journal in place
// and reports it (report.Failed), rather than silently discarding an op recovery cannot
// complete: the conservative posture of HEAL-4 is to surface what it does not understand,
// never to drop a possibly-committed op.
//
// The returned closure captures ctx/l/g so the executor can invoke it with only an
// OpRecovery (the journal.Finisher signature). It must stay offline — every route it
// dispatches to is an offline domain operation.
func Finisher(ctx context.Context, l layout.Layout, g *git.Git) journal.Finisher {
	return func(op journal.OpRecovery) error {
		switch op.Kind {
		case journal.KindIsolateRm:
			return isolate.FinishRemove(ctx, l, g, op)
		case journal.KindLand:
			return finishLand(ctx, l, g, op)
		default:
			return fmt.Errorf("recovery: no finisher for kind %q (op %s) — journal left for retry", op.Kind, op.OpID)
		}
	}
}

// finishLand routes a rolled-forward land op to its domain Finisher. The journal carries the
// repo NAMES but not their bases (the landstate record stores shas, not base branch names), so
// — exactly as `wi land continue` does — recovery resolves each name's base from the manifest
// before handing pre-resolved specs to land.FinishLand. This is where the manifest dependency
// lives so the land domain stays decoupled from manifest parsing. A manifest that is missing,
// malformed, or no longer declares a journaled repo means recovery cannot safely resolve a
// base: it returns an error so the executor LEAVES the journal for the next startup (the
// conservative HEAL-4 posture), never landing onto a guessed base.
func finishLand(ctx context.Context, l layout.Layout, g *git.Git, op journal.OpRecovery) error {
	cfg, err := config.Load(l.Config())
	if err != nil {
		return fmt.Errorf("recovery: load manifest to resume land %q (op %s): %w", op.Task, op.OpID, err)
	}
	specs := make([]land.RepoSpec, 0, len(op.Repos))
	for _, name := range op.Repos {
		r, ok := cfg.Lookup(name)
		if !ok {
			return fmt.Errorf("recovery: repo %q in journaled land %q (op %s) is no longer declared in the manifest — journal left for retry", name, op.Task, op.OpID)
		}
		specs = append(specs, land.RepoSpec{Name: r.Name, Base: baseref.Resolve(ctx, g, l, r.Name, r.Base)})
	}
	return land.FinishLand(ctx, l, g, op.Task, op.OpID, specs)
}
