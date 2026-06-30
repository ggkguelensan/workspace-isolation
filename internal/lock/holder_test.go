package lock

import (
	"os"
	"strings"
	"testing"
)

// Guard LOCK-HOLDER (M4): the lock-holder identity {pid,host,boot_id,op_id} must
// round-trip through its serialization without loss, and CurrentHolder must
// capture THIS process and THIS boot, because the lock-liveness layer reads a
// recorded holder back and compares its boot_id+pid against the live machine to
// decide whether a held lock is stale (DESIGN §6 / §7.3). A lossy serialization
// or a mis-captured identity would make every staleness verdict unreliable — and
// a wrong verdict either wedges a lock forever or steals it from a live peer.
//
// Non-vacuity mutant (registered): in CurrentHolder ignore the opID argument and
// set OpID:"" → TestCurrentHolderCapturesProcess RED (OpID mismatch). Alternate:
// rename a json tag (e.g. `boot_id`→`bootid`) on Holder → the round-trip drops
// BootID → TestHolderRoundTrip RED. Confirmed RED before green.
func TestHolderRoundTrip(t *testing.T) {
	want := Holder{PID: 4242, Host: "node-7", BootID: "boot_id:abc-123", OpID: "op-20260630-xyz"}
	b, err := want.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// The body is a single JSON line (trailing newline) so a lock file is one
	// clean record on disk.
	if !strings.HasSuffix(string(b), "\n") || strings.Count(strings.TrimRight(string(b), "\n"), "\n") != 0 {
		t.Errorf("Marshal not a single newline-terminated line: %q", string(b))
	}
	// Pin the DURABLE wire field names. A round-trip alone can't catch a json-tag
	// rename (Marshal and Unmarshal share the tag, so they stay symmetric), yet a
	// rename silently breaks cross-build lock compatibility — a lock file written
	// by one wi build must read in another. Assert the concrete keys so a tag
	// change reddens here.
	for _, key := range []string{`"pid"`, `"host"`, `"boot_id"`, `"op_id"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("marshaled holder %q missing durable wire key %s", string(b), key)
		}
	}
	got, err := ParseHolder(b)
	if err != nil {
		t.Fatalf("ParseHolder: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestCurrentHolderCapturesProcess(t *testing.T) {
	h, err := CurrentHolder("op-xyz")
	if err != nil {
		t.Fatalf("CurrentHolder: %v", err)
	}
	if h.PID != os.Getpid() {
		t.Errorf("PID = %d, want this process %d", h.PID, os.Getpid())
	}
	if h.OpID != "op-xyz" {
		t.Errorf("OpID = %q, want %q", h.OpID, "op-xyz")
	}
	if h.Host == "" {
		t.Error("Host is empty, want this machine's hostname")
	}
	if h.BootID == "" {
		t.Error("BootID is empty, want this boot's identifier")
	}
}

func TestParseHolderRejectsUnusable(t *testing.T) {
	// An empty body is a real on-disk state: flock creates the lock file before
	// any holder body is written. The liveness policy must treat it as "unknown
	// holder" (never break), so Parse surfaces it as an error rather than a
	// zero-value Holder that could be mistaken for a real pid-0 holder.
	if _, err := ParseHolder(nil); err == nil {
		t.Error("ParseHolder(nil) = nil error, want an error on empty input")
	}
	if _, err := ParseHolder([]byte("   \n")); err == nil {
		t.Error("ParseHolder(whitespace) = nil error, want an error on blank input")
	}
	if _, err := ParseHolder([]byte("{not json")); err == nil {
		t.Error("ParseHolder(garbage) = nil error, want a parse error")
	}
}
