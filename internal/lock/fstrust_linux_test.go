//go:build linux

package lock

import "testing"

// Guard LOCK-FS-TRUST (M4 self-heal, DESIGN §7.3): the fs-trust gate. See the
// darwin sibling for the full rationale. wi may auto-break a stale lock ONLY on a
// filesystem where flock(2) is known reliable, so the classifier is an allowlist of
// known-local superblock magics (fail-closed): a network fs (NFS/CIFS/9p/FUSE) or an
// unrecognized magic is NOT trustworthy.
//
// Non-vacuity mutant (registered): make linuxFSTypeTrustworthy `return true`
// unconditionally → the network/unknown magics (NFS 0x6969, CIFS, 9p, FUSE, 0) read
// as trustworthy → RED. Alternate: `return false` → the local ext/btrfs/xfs magics
// redden. The dangerous direction is trusting a network fs.
func TestLinuxFSTypeTrustworthy(t *testing.T) {
	for _, m := range []int64{magicEXT, magicBTRFS, magicXFS, magicTMPFS, magicOVERLAYFS, magicEXFAT} {
		if !linuxFSTypeTrustworthy(m) {
			t.Errorf("linuxFSTypeTrustworthy(%#x) = false, want true (local fs)", m)
		}
	}
	for _, m := range []int64{
		0x6969,     // NFS
		0xFF534D42, // SMB/CIFS
		0x01021997, // 9p
		0x65735546, // FUSE
		0,          // unknown
	} {
		if linuxFSTypeTrustworthy(m) {
			t.Errorf("linuxFSTypeTrustworthy(%#x) = true, want false (network/unknown fs)", m)
		}
	}
}

// FSTrustworthy wires statfs → classifier end-to-end; a real local temp dir (ext4 or
// tmpfs on typical Linux CI) must be trusted.
func TestFSTrustworthyLocalTempDir(t *testing.T) {
	ok, err := FSTrustworthy(t.TempDir())
	if err != nil {
		t.Fatalf("FSTrustworthy(tempdir): %v", err)
	}
	if !ok {
		t.Errorf("FSTrustworthy(local temp dir) = false, want true")
	}
}
