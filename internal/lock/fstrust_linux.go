//go:build linux

package lock

import (
	"fmt"
	"syscall"
)

// Linux superblock magic numbers (linux/magic.h) for filesystems where flock(2) is
// locally reliable. Remote/network filesystems — NFS 0x6969, SMB/CIFS 0xFF534D42,
// 9p 0x01021997, FUSE 0x65735546, CEPH, GlusterFS, … — are deliberately ABSENT: the
// allowlist fails closed, so an unrecognized magic is not trustworthy.
const (
	magicEXT       = 0xEF53
	magicBTRFS     = 0x9123683E
	magicXFS       = 0x58465342
	magicTMPFS     = 0x01021994
	magicRAMFS     = 0x858458F6
	magicF2FS      = 0xF2F52010
	magicZFS       = 0x2FC12FC1
	magicOVERLAYFS = 0x794C7630
	magicEXFAT     = 0x2011BAB0
	magicVFAT      = 0x4D44
)

// linuxFSTypeTrustworthy classifies a statfs f_type magic as a known-local fs.
func linuxFSTypeTrustworthy(magic int64) bool {
	switch magic {
	case magicEXT, magicBTRFS, magicXFS, magicTMPFS, magicRAMFS,
		magicF2FS, magicZFS, magicOVERLAYFS, magicEXFAT, magicVFAT:
		return true
	default:
		return false
	}
}

// fsTrustworthy resolves path's filesystem via statfs(2) and classifies its magic.
func fsTrustworthy(path string) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false, fmt.Errorf("lock: statfs %q: %w", path, err)
	}
	return linuxFSTypeTrustworthy(int64(st.Type)), nil
}
