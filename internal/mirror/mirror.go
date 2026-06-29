// Package mirror owns wi's cached SSOT freshness metadata (DESIGN §5). A
// Snapshot records what the last fetch observed about a repo's local mirror base
// versus origin; it is persisted under <root>/.wi/mirrors/<repo>.json and read
// back with NO network I/O. Read paths classify freshness purely from the cached
// snapshot — wi never auto-fetches, so the cached count is the most current
// offline-knowable signal — which is how the no-hidden-network invariant
// (DESIGN §2 #3) is honored on the freshness read path. Only the separate fetch
// path (its own unit) dials; nothing in this file imports git/gitexec or takes a
// Runner, so a read here structurally cannot reach the network.
package mirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
)

// ErrNoSnapshot reports that a repo has no cached freshness snapshot yet (it has
// never been fetched). Callers surface this as an absent mirror_freshness block,
// NOT as staleness.
var ErrNoSnapshot = errors.New("mirror: no freshness snapshot")

// Snapshot is the cached result of the last fetch for one repo's SSOT mirror.
// All fields are scalar so a Snapshot is comparable. Counts are "as of
// FetchedAt"; read paths consult them without dialing.
type Snapshot struct {
	Repo                  string `json:"repo"`
	Base                  string `json:"base"`
	FetchedAt             string `json:"fetched_at"`
	LocalBaseSHA          string `json:"local_base_sha"`
	OriginBaseSHA         string `json:"origin_base_sha"`
	BehindOriginAsOfFetch int    `json:"behind_origin_as_of_fetch"`
}

// Freshness projects the cached snapshot onto the wire contract's
// mirror_freshness block (contract.MirrorFreshness). It performs no I/O. A
// mirror is stale exactly when the last fetch saw the local base behind origin
// (BehindOriginAsOfFetch > 0): wi never auto-fetches, so this cached count is
// the most current offline-knowable freshness signal (DESIGN §3.3, §5).
func (s Snapshot) Freshness() contract.MirrorFreshness {
	return contract.MirrorFreshness{
		Stale:                 s.BehindOriginAsOfFetch > 0,
		FetchedAt:             s.FetchedAt,
		BehindOriginAsOfFetch: s.BehindOriginAsOfFetch,
	}
}

// metaPath returns the snapshot file for repo within mirrorsDir
// (layout.MirrorsDir()). The repo name is validated through layout's single
// traversal chokepoint because it becomes a filename — mirror owns the
// "<repo>.json" naming within the layout-provided directory, the same way lock
// owns its "<key>.lock" naming within layout.LocksDir().
func metaPath(mirrorsDir, repo string) (string, error) {
	if err := layout.ValidateSegment("repo", repo); err != nil {
		return "", err
	}
	return filepath.Join(mirrorsDir, repo+".json"), nil
}

// Load reads the cached snapshot for repo from mirrorsDir. It is a pure local
// read (no network). A repo that has never been fetched yields ErrNoSnapshot.
func Load(mirrorsDir, repo string) (Snapshot, error) {
	p, err := metaPath(mirrorsDir, repo)
	if err != nil {
		return Snapshot{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Snapshot{}, ErrNoSnapshot
		}
		return Snapshot{}, fmt.Errorf("mirror: read snapshot %s: %w", p, err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return Snapshot{}, fmt.Errorf("mirror: parse snapshot %s: %w", p, err)
	}
	return s, nil
}

// Store atomically persists s under mirrorsDir via the single .wi/ atomic writer
// (DESIGN §6.2). mirrorsDir must already exist (layout.Bootstrap creates it).
func Store(mirrorsDir string, s Snapshot) error {
	p, err := metaPath(mirrorsDir, s.Repo)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("mirror: marshal snapshot for %q: %w", s.Repo, err)
	}
	data = append(data, '\n')
	if err := lockfs.WriteFileAtomic(p, data, 0o644); err != nil {
		return fmt.Errorf("mirror: write snapshot %s: %w", p, err)
	}
	return nil
}
