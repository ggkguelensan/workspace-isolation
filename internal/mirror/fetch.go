package mirror

import (
	"context"
	"fmt"
	"time"

	"github.com/ggkguelensan/workspace-isolation/internal/clock"
	"github.com/ggkguelensan/workspace-isolation/internal/git"
)

// Refresh performs the one network step of the freshness layer: it fetches origin
// into the repo's SSOT mirror at dir, recomputes how far the local base trails
// origin from LOCAL refs (no second dial), persists a fresh Snapshot under
// mirrorsDir, and returns it.
//
// This is the ONLY part of mirror that touches git or the network — which is why
// it takes a *git.Git and a clock.Clock, while the read path (Load/Freshness)
// takes neither and so structurally cannot dial. Refresh deliberately does NOT
// advance the base ref: git.Fetch only updates the remote-tracking ref, and
// Refresh merely records what that fetch observed. Advancing refs/heads/<base> is
// FastForwardBaseRef's exclusive job on the sync path (DESIGN §5), so the SSOT
// working tree stays pristine across a Refresh.
func Refresh(ctx context.Context, g *git.Git, clk clock.Clock, mirrorsDir, repo, dir, base string) (Snapshot, error) {
	localRef := "refs/heads/" + base
	originRef := "refs/remotes/origin/" + base

	if err := g.Fetch(ctx, dir, "origin"); err != nil {
		return Snapshot{}, fmt.Errorf("mirror: refresh %s: %w", repo, err)
	}
	localSHA, err := g.ResolveRef(ctx, dir, localRef)
	if err != nil {
		return Snapshot{}, fmt.Errorf("mirror: refresh %s: %w", repo, err)
	}
	originSHA, err := g.ResolveRef(ctx, dir, originRef)
	if err != nil {
		return Snapshot{}, fmt.Errorf("mirror: refresh %s: %w", repo, err)
	}
	// Behind = commits in origin's base not yet in the local base (the right
	// column of DivergedCounts); ahead is irrelevant to freshness.
	_, behind, err := g.DivergedCounts(ctx, dir, localRef, originRef)
	if err != nil {
		return Snapshot{}, fmt.Errorf("mirror: refresh %s: %w", repo, err)
	}

	s := Snapshot{
		Repo:                  repo,
		Base:                  base,
		FetchedAt:             clk.Now().UTC().Format(time.RFC3339),
		LocalBaseSHA:          localSHA,
		OriginBaseSHA:         originSHA,
		BehindOriginAsOfFetch: behind,
	}
	if err := Store(mirrorsDir, s); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}
