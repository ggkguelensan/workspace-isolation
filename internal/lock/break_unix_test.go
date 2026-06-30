//go:build unix

package lock

import (
	"os"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/host"
)

// Guard LOCK-BREAK (M4 self-heal, DESIGN §7.3 / HEAL-3): Break is the ACTION counterpart
// to List — it displaces a stale lock, but ONLY when the read-only AssessBreak gate
// returns Safe (holder PROVEN DEAD on a flock-trustworthy fs). The load-bearing property
// is the SAFE GATE: a live holder, an unknown/body-less holder, or an untrustworthy fs
// must leave the lock file untouched (caller maps that to exit 6 lock_held); only a
// proven-dead holder's stale file is unlinked so the next Acquire starts clean. Unlinking
// a file a live peer still holds would break mutual exclusion (a later Acquire O_CREATEs a
// new inode and flocks THAT) — the exact data-loss path DESIGN §7 forbids.
//
// Non-vacuity mutant (registered): drop the `if !d.Safe { return d, nil }` early return so
// Break ALWAYS unlinks the lock file regardless of verdict → the live-holder and
// unknown-holder subtests RED (their lock file is destroyed though they were refused),
// while the proven-dead and nothing-to-break subtests stay green. Alternate: replace
// os.Remove with a no-op → the proven-dead subtest reddens (file survives a safe break).
func TestBreak(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	bootID, err := host.BootID()
	if err != nil {
		t.Fatalf("boot id: %v", err)
	}

	writeBody := func(t *testing.T, locksDir string, k Key, h Holder) {
		t.Helper()
		body, err := h.Marshal()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(k.Path(locksDir), body, 0o600); err != nil {
			t.Fatalf("write %s: %v", k.String(), err)
		}
	}

	t.Run("proven-dead holder on trustworthy fs is broken", func(t *testing.T) {
		locksDir := t.TempDir() // trustworthy fs (apfs/ext on a typical dev host)
		api, _ := Repo("api")
		writeBody(t, locksDir, api, Holder{PID: 999999, Host: hostname, BootID: bootID + "-stale", OpID: "op_dead"})

		d, err := Break(locksDir, api)
		if err != nil {
			t.Fatalf("Break: %v", err)
		}
		if !d.Safe || !d.ProvenDead || !d.HolderKnown {
			t.Errorf("decision = %+v, want Safe+ProvenDead+HolderKnown", d)
		}
		if _, err := os.Stat(api.Path(locksDir)); !os.IsNotExist(err) {
			t.Errorf("lock file still present after a safe break (stat err = %v), want removed", err)
		}
	})

	t.Run("live holder is refused and left intact", func(t *testing.T) {
		locksDir := t.TempDir()
		task1, _ := IsolateState("task1")
		live, err := CurrentHolder("op_live")
		if err != nil {
			t.Fatalf("CurrentHolder: %v", err)
		}
		writeBody(t, locksDir, task1, live)

		d, err := Break(locksDir, task1)
		if err != nil {
			t.Fatalf("Break: %v", err)
		}
		if d.Safe || d.ProvenDead {
			t.Errorf("decision = %+v, want not Safe (live holder)", d)
		}
		if _, err := os.Stat(task1.Path(locksDir)); err != nil {
			t.Errorf("lock file removed for a live holder (stat err = %v), want intact", err)
		}
	})

	t.Run("unknown holder (body-less) is refused and left intact", func(t *testing.T) {
		locksDir := t.TempDir()
		reg := ProjectRegistry()
		if err := os.WriteFile(reg.Path(locksDir), nil, 0o600); err != nil {
			t.Fatalf("write empty registry lock: %v", err)
		}

		d, err := Break(locksDir, reg)
		if err != nil {
			t.Fatalf("Break: %v", err)
		}
		if d.Safe || d.HolderKnown {
			t.Errorf("decision = %+v, want unknown holder, not Safe (body-less lock)", d)
		}
		if _, err := os.Stat(reg.Path(locksDir)); err != nil {
			t.Errorf("body-less lock file removed (stat err = %v), want intact", err)
		}
	})

	t.Run("nothing to break is not an error", func(t *testing.T) {
		locksDir := t.TempDir()
		api, _ := Repo("api")
		d, err := Break(locksDir, api) // no lock file present at all
		if err != nil {
			t.Fatalf("Break(missing) error = %v, want nil (nothing to break)", err)
		}
		if d.Safe || d.HolderKnown {
			t.Errorf("decision = %+v, want unknown/not-safe for a missing lock", d)
		}
	})
}
