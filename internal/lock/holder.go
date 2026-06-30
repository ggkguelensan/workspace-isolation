package lock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ggkguelensan/workspace-isolation/internal/host"
)

// Holder records WHO holds a lock — the identity wi writes into a lock file's
// body on acquire and reads back to reason about staleness (DESIGN §6 / §7.3).
// The four fields are the lock-liveness reuse key: {boot_id, pid} uniquely names
// a process on this machine across reboots (a pid is only meaningful within the
// boot that minted it, so boot_id guards against pid reuse after a restart);
// host disambiguates a lock dir that might be shared over a network filesystem;
// and op_id correlates the lock with the operation — and its journal and
// envelope — that took it (CTX-OPID). All fields are comparable, so two holders
// compare with ==.
type Holder struct {
	PID    int    `json:"pid"`
	Host   string `json:"host"`
	BootID string `json:"boot_id"`
	OpID   string `json:"op_id"`
}

// Marshal renders the holder as a single newline-terminated JSON line, suitable
// for writing as a lock file's body. The encoding is stable (encoding/json emits
// struct fields in declaration order), so a lock body written by one wi build
// reads back identically in another.
func (h Holder) Marshal() ([]byte, error) {
	b, err := json.Marshal(h)
	if err != nil {
		return nil, fmt.Errorf("lock: marshal holder: %w", err)
	}
	return append(b, '\n'), nil
}

// ParseHolder decodes a lock file body produced by Marshal. Empty, blank, or
// malformed input is an error — never a zero-value Holder — because the caller
// (the liveness policy) must treat an unreadable body conservatively: an unknown
// holder is never assumed dead, so a lock is never broken on a body we cannot
// understand. An empty body is a real state: flock(2) creates the lock file
// before any holder body has been written.
func ParseHolder(b []byte) (Holder, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return Holder{}, fmt.Errorf("lock: empty holder body")
	}
	var h Holder
	if err := json.Unmarshal(b, &h); err != nil {
		return Holder{}, fmt.Errorf("lock: parse holder: %w", err)
	}
	return h, nil
}

// CurrentHolder captures the identity of THIS process and THIS boot for opID, the
// correlation id minted for the operation taking the lock (CTX-OPID). It is what
// gets written when a lock is acquired; a later reader compares the recorded
// boot_id and pid against the live machine (host.BootID + the proven-dead pid
// gate) to decide whether the holder is stale.
func CurrentHolder(opID string) (Holder, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return Holder{}, fmt.Errorf("lock: hostname: %w", err)
	}
	bootID, err := host.BootID()
	if err != nil {
		return Holder{}, fmt.Errorf("lock: boot id: %w", err)
	}
	return Holder{
		PID:    os.Getpid(),
		Host:   hostname,
		BootID: bootID,
		OpID:   opID,
	}, nil
}
