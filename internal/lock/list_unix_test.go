//go:build unix

package lock

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/host"
)

// Guard LOCK-LIST (M4 self-heal, DESIGN §7.3 / §7.4): List enumerates every lock present
// in a locks dir and assesses each (AssessBreak), returning one LockStatus per recognized
// lock file, sorted by key. The load-bearing properties: (a) it reconstructs each lock's
// key via ParseKey and SILENTLY SKIPS any file that is not a "<key>.lock" for a key in the
// closed namespace — a stray file must NEVER be fabricated into a lock; (b) a missing
// locks dir is the empty result, not an error ("no locks" is a valid state); (c) each
// recognized lock carries its AssessBreak verdict.
//
// Non-vacuity mutant (registered): drop the stray-skip — `key, _ := ParseKey(...)` with no
// `continue` on error — so an unparseable filename (e.g. "notakey.lock") yields the zero
// Key and is assessed and appended anyway → a phantom LockStatus with an empty key appears
// in the result → TestList RED on the exact-key-set assertion. Alternate: drop the
// os.ErrNotExist special-case in List → a missing dir returns an error → the missing-dir
// case reddens.
func TestList(t *testing.T) {
	t.Run("missing dir is empty, not an error", func(t *testing.T) {
		got, err := List(filepath.Join(t.TempDir(), "never-created"))
		if err != nil {
			t.Fatalf("List(missing) error = %v, want nil (no locks is a valid state)", err)
		}
		if len(got) != 0 {
			t.Errorf("List(missing) = %v, want empty", got)
		}
	})

	t.Run("empty dir is empty", func(t *testing.T) {
		got, err := List(t.TempDir())
		if err != nil {
			t.Fatalf("List(empty) error = %v", err)
		}
		if len(got) != 0 {
			t.Errorf("List(empty) = %v, want empty", got)
		}
	})

	t.Run("enumerates, assesses, sorts, and skips strays", func(t *testing.T) {
		hostname, err := os.Hostname()
		if err != nil {
			t.Fatalf("hostname: %v", err)
		}
		bootID, err := host.BootID()
		if err != nil {
			t.Fatalf("boot id: %v", err)
		}
		locksDir := t.TempDir() // trustworthy fs (apfs/ext on a typical dev host)

		writeBody := func(t *testing.T, k Key, h Holder) {
			t.Helper()
			body, err := h.Marshal()
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := os.WriteFile(k.Path(locksDir), body, 0o600); err != nil {
				t.Fatalf("write %s: %v", k.String(), err)
			}
		}

		// A proven-dead holder (same host, boot-mismatched) on a trustworthy fs → Safe.
		api, _ := Repo("api")
		writeBody(t, api, Holder{PID: 999999, Host: hostname, BootID: bootID + "-stale", OpID: "op_dead"})

		// A live holder (this process) → known but not breakable.
		task1, _ := IsolateState("task1")
		live, err := CurrentHolder("op_live")
		if err != nil {
			t.Fatalf("CurrentHolder: %v", err)
		}
		writeBody(t, task1, live)

		// A body-less lock (acquired but never stamped) → unknown holder, not breakable.
		reg := ProjectRegistry()
		if err := os.WriteFile(reg.Path(locksDir), nil, 0o600); err != nil {
			t.Fatalf("write empty registry lock: %v", err)
		}

		// Strays that must NOT appear: a non-.lock file, a .lock file whose stem is not a
		// valid key, and a subdirectory.
		if err := os.WriteFile(filepath.Join(locksDir, "README.txt"), []byte("hi"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(locksDir, "notakey.lock"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(locksDir, "sub.lock"), 0o755); err != nil {
			t.Fatal(err)
		}

		list, err := List(locksDir)
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		var gotKeys []string
		byKey := map[string]BreakDecision{}
		for _, s := range list {
			gotKeys = append(gotKeys, s.Key.String())
			byKey[s.Key.String()] = s.Decision
		}
		wantKeys := []string{"isolate-state:task1", "project-registry", "repo:api"}
		if !reflect.DeepEqual(gotKeys, wantKeys) {
			t.Fatalf("List keys = %v, want %v (sorted, strays skipped)", gotKeys, wantKeys)
		}

		if d := byKey["repo:api"]; !d.Safe || !d.ProvenDead || !d.HolderKnown {
			t.Errorf("repo:api decision = %+v, want Safe+ProvenDead+HolderKnown (proven-dead on trustworthy fs)", d)
		}
		if d := byKey["isolate-state:task1"]; d.Safe || d.ProvenDead || !d.HolderKnown {
			t.Errorf("isolate-state:task1 decision = %+v, want HolderKnown but not Safe/ProvenDead (live holder)", d)
		}
		if d := byKey["project-registry"]; d.Safe || d.HolderKnown {
			t.Errorf("project-registry decision = %+v, want unknown holder, not breakable (body-less lock)", d)
		}
	})
}
