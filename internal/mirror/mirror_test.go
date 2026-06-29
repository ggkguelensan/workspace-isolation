package mirror_test

import (
	"errors"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/mirror"
)

// Guard MIRROR-FRESHNESS — the offline staleness predicate (DESIGN §3.3, §5).
//
// A Snapshot is the cached result of the last fetch. Freshness() projects it
// onto the wire contract's mirror_freshness block with NO I/O. wi never
// auto-fetches, so the cached "behind origin as of fetch" count IS the most
// current offline-knowable signal: a mirror is stale exactly when that count is
// > 0 (the local base was behind origin at the last fetch). The bool and the
// count are non-redundant — behind_origin_as_of_fetch is ,omitempty (absent at
// 0), so the explicit stale bool is the stable signal agents branch on.
//
// Two-sided, so any constant mutant fails: behind>0 ⇒ stale; behind==0 ⇒ fresh.
//
// Non-vacuity: hardcode Stale:false in Freshness() → the behind>0 case RED.

func TestFreshnessClassifiesStaleByBehindCount(t *testing.T) {
	behind := mirror.Snapshot{
		Repo: "acme", Base: "main",
		FetchedAt:             "2026-06-30T00:00:00Z",
		BehindOriginAsOfFetch: 3,
	}
	f := behind.Freshness()
	if !f.Stale {
		t.Errorf("behind=3 → Stale=false, want true")
	}
	if f.BehindOriginAsOfFetch != 3 {
		t.Errorf("BehindOriginAsOfFetch = %d, want 3", f.BehindOriginAsOfFetch)
	}
	if f.FetchedAt != "2026-06-30T00:00:00Z" {
		t.Errorf("FetchedAt = %q, want it carried through", f.FetchedAt)
	}

	fresh := mirror.Snapshot{
		Repo: "acme", Base: "main",
		FetchedAt:             "2026-06-30T00:00:00Z",
		BehindOriginAsOfFetch: 0,
	}
	if fresh.Freshness().Stale {
		t.Errorf("behind=0 → Stale=true, want false (up to date as of last fetch)")
	}
}

// Guard MIRROR-PERSIST — the cached-snapshot store/load round-trip (DESIGN §5,
// §6.2). Store is an atomic .wi/ write (lockfs.WriteFileAtomic); Load is a pure
// local read that never dials. A repo never fetched yields ErrNoSnapshot (the
// caller omits the mirror_freshness block rather than reporting it stale), and
// the repo name — which becomes a filename — is validated through layout's
// single traversal chokepoint.
//
// Non-vacuity: make Store a no-op (return nil without writing) → Load reports
// ErrNoSnapshot → TestSnapshotRoundTrips RED.

func TestSnapshotRoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := mirror.Snapshot{
		Repo: "acme", Base: "main",
		FetchedAt:             "2026-06-30T00:00:00Z",
		LocalBaseSHA:          "1111111111111111111111111111111111111111",
		OriginBaseSHA:         "2222222222222222222222222222222222222222",
		BehindOriginAsOfFetch: 2,
	}
	if err := mirror.Store(dir, want); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := mirror.Load(dir, "acme")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got = %+v\nwant = %+v", got, want)
	}
}

func TestLoadMissingIsErrNoSnapshot(t *testing.T) {
	dir := t.TempDir()
	_, err := mirror.Load(dir, "never-fetched")
	if !errors.Is(err, mirror.ErrNoSnapshot) {
		t.Errorf("Load of an unfetched repo err = %v, want mirror.ErrNoSnapshot", err)
	}
}

func TestStoreRejectsUnsafeRepoName(t *testing.T) {
	dir := t.TempDir()
	err := mirror.Store(dir, mirror.Snapshot{Repo: "../escape", Base: "main"})
	if err == nil {
		t.Errorf("Store accepted a traversing repo name; the segment chokepoint must reject it")
	}
}
