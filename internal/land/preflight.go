package land

import (
	"context"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/git"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// RepoPreflight is one repo's non-mutating land pre-check (a Preflight result): whether its
// committed isolate work would fast-forward onto its base, WITHOUT moving anything. WouldLand
// is git.IsAncestor(BaseTip, WorkTip) — true iff the base tip is an ancestor of the work tip
// (a clean fast-forward). BaseTip/WorkTip are the resolved shas so the caller can report
// exactly what it inspected.
type RepoPreflight struct {
	Repo      string
	Base      string
	BaseTip   string
	WorkTip   string
	WouldLand bool
}

// Preflight checks, WITHOUT MOVING ANYTHING, whether every repo's isolate work would
// fast-forward onto its base — the all-or-nothing gate `wi land --atomic` consults before the
// first pointer move (DESIGN §7.2). For each spec it resolves the worktree HEAD (the work tip)
// and the base ref tip and calls git.IsAncestor(baseTip, workTip); it writes NO backup ref,
// advances NO base ref, persists NO landstate record, and takes NO lock — it is a pure read
// the atomic orchestrator runs under its own already-held lock. It returns a per-repo verdict
// in request order and ok=true IFF EVERY repo would land. It does NOT short-circuit on the
// first non-fast-forward: every repo is checked so the caller can report the full set of
// blockers, not just the first. A genuine infra fault (an unresolvable ref or worktree) is a
// Go error, distinct from a clean would-not-fast-forward (a recorded WouldLand=false, not an
// error) — exactly the refusal/fault split LandRepo draws.
func Preflight(ctx context.Context, g *git.Git, l layout.Layout, task string, specs []RepoSpec) (checks []RepoPreflight, ok bool, err error) {
	checks = make([]RepoPreflight, 0, len(specs))
	ok = true
	for _, s := range specs {
		ssot, lerr := l.Repo(s.Name)
		if lerr != nil {
			return checks, false, fmt.Errorf("land preflight: locate ssot for %s: %w", s.Name, lerr)
		}
		wt, lerr := l.Isolate(task, s.Name)
		if lerr != nil {
			return checks, false, fmt.Errorf("land preflight: locate worktree for %s: %w", s.Name, lerr)
		}
		workTip, rerr := g.ResolveRef(ctx, wt, "HEAD")
		if rerr != nil {
			return checks, false, fmt.Errorf("land preflight: resolve work tip for %s: %w", s.Name, rerr)
		}
		baseTip, rerr := g.ResolveRef(ctx, ssot, "refs/heads/"+s.Base)
		if rerr != nil {
			return checks, false, fmt.Errorf("land preflight: resolve base %q for %s: %w", s.Base, s.Name, rerr)
		}
		// IsAncestor runs in the SSOT clone, whose object store the worktree shares
		// (DESIGN §1), so both tips are reachable there — the same dir FastForwardBaseRef
		// resolves the work tip in, so the gate and the advance agree by construction.
		ff, aerr := g.IsAncestor(ctx, ssot, baseTip, workTip)
		if aerr != nil {
			return checks, false, fmt.Errorf("land preflight: ancestry for %s: %w", s.Name, aerr)
		}
		checks = append(checks, RepoPreflight{Repo: s.Name, Base: s.Base, BaseTip: baseTip, WorkTip: workTip, WouldLand: ff})
		if !ff {
			ok = false
		}
	}
	return checks, ok, nil
}
