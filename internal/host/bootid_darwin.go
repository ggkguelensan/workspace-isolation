//go:build darwin

package host

import (
	"encoding/binary"
	"fmt"
	"syscall"
)

// bootID derives a per-boot identifier on darwin from the kern.boottime sysctl
// (open decision #3 mandates sysctl, not /proc, on darwin). It invokes the
// sysctl(2) syscall directly through the standard library — NOT a child process —
// so it adds no dependency (decision #6) AND spawns no subprocess, honoring
// INV-NO-NETWORK (only internal/gitexec may import os/exec, so the no-network
// egress belt stays unbypassable; DESIGN §2 #3). A syscall dials no network.
//
// kern.boottime is a `struct timeval { int64 tv_sec; int64 tv_usec }` holding the
// wall-clock instant the kernel booted. tv_sec is set once at boot, is not moved
// by sleep/wake, and differs after a reboot — exactly the reuse guard the
// lock-liveness layer needs to tell a current-boot pid from one recorded before a
// reboot.
func bootID() (string, error) {
	// syscall.Sysctl returns the raw value bytes as a string. For kern.boottime
	// that is the timeval in host (little-endian on all darwin archs) byte order.
	// We need only tv_sec: it occupies the first 8 bytes and is untouched by
	// Sysctl's single trailing-NUL trim, so decoding the leading 8 bytes is robust
	// even though the value carries interior NULs (the high bytes of tv_sec).
	raw, err := syscall.Sysctl("kern.boottime")
	if err != nil {
		return "", fmt.Errorf("host: sysctl kern.boottime: %w", err)
	}
	if len(raw) < 8 {
		return "", fmt.Errorf("host: kern.boottime returned %d bytes, need >= 8", len(raw))
	}
	sec := binary.LittleEndian.Uint64([]byte(raw)[:8])
	if sec == 0 {
		return "", fmt.Errorf("host: kern.boottime tv_sec is zero")
	}
	return fmt.Sprintf("boottime:%d", sec), nil
}
