//go:build darwin

package lock

import "testing"

// Guard LOCK-FS-TRUST (M4 self-heal, DESIGN §7.3): the fs-trust gate. wi may
// auto-break a stale lock ONLY on a filesystem where flock(2) is known reliable, so
// the classifier must POSITIVELY recognize a local fs and refuse (return false) for
// anything else — above all a network fs (NFS/SMB/AFP/WebDAV), where another HOST
// could hold the same flock and a break would corrupt mutual exclusion. It is
// therefore an allowlist (fail-closed): an unknown or empty type is NOT trustworthy.
//
// Non-vacuity mutant (registered): make darwinFSTypeTrustworthy `return true`
// unconditionally → the network/unknown cases (nfs, smbfs, "") read as trustworthy
// → RED. Alternate: `return false` → the local apfs/hfs cases redden. The dangerous
// direction is trusting a network fs, so that is the load-bearing half.
func TestDarwinFSTypeTrustworthy(t *testing.T) {
	for _, name := range []string{"apfs", "hfs", "ufs", "msdos", "exfat"} {
		if !darwinFSTypeTrustworthy(name) {
			t.Errorf("darwinFSTypeTrustworthy(%q) = false, want true (local fs)", name)
		}
	}
	for _, name := range []string{"nfs", "smbfs", "afpfs", "webdav", "ftp", "fusefs", "", "wat"} {
		if darwinFSTypeTrustworthy(name) {
			t.Errorf("darwinFSTypeTrustworthy(%q) = true, want false (network/unknown fs)", name)
		}
	}
}

// FSTrustworthy wires Statfs → classifier end-to-end; a real local temp dir (apfs on
// a modern mac) must be trusted, proving the syscall path and string extraction work.
func TestFSTrustworthyLocalTempDir(t *testing.T) {
	ok, err := FSTrustworthy(t.TempDir())
	if err != nil {
		t.Fatalf("FSTrustworthy(tempdir): %v", err)
	}
	if !ok {
		t.Errorf("FSTrustworthy(local temp dir) = false, want true")
	}
}
