//go:build darwin

package lock

import (
	"fmt"
	"syscall"
)

// darwinFSTypeTrustworthy is the allowlist of darwin filesystem type names
// (statfs f_fstypename) on which flock(2) is locally reliable. Network filesystems
// — nfs, smbfs, afpfs, webdav, ftp, fusefs — are deliberately ABSENT: the allowlist
// fails closed, so any unrecognized or empty name is not trustworthy.
func darwinFSTypeTrustworthy(name string) bool {
	switch name {
	case "apfs", "hfs", "ufs", "msdos", "exfat":
		return true
	default:
		return false
	}
}

// fsTrustworthy resolves path's filesystem via statfs(2) and classifies its type.
func fsTrustworthy(path string) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false, fmt.Errorf("lock: statfs %q: %w", path, err)
	}
	return darwinFSTypeTrustworthy(fstypename(&st)), nil
}

// fstypename extracts the NUL-terminated f_fstypename ([16]int8) as a Go string.
func fstypename(st *syscall.Statfs_t) string {
	b := st.Fstypename[:]
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = byte(b[i])
	}
	return string(out)
}
