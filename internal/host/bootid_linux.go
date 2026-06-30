//go:build linux

package host

import (
	"fmt"
	"os"
	"strings"
)

// bootID derives a per-boot identifier on linux from
// /proc/sys/kernel/random/boot_id — a random UUID the kernel generates fresh on
// every boot and holds constant for the boot's lifetime (sleep/wake does not
// change it). That is exactly the reuse guard the lock-liveness layer needs: a
// lock body recording a different boot_id was written before a reboot, so its pid
// is meaningless. Reading a /proc file dials no network, honoring DESIGN §2.
func bootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("host: read boot_id: %w", err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", fmt.Errorf("host: empty boot_id from /proc/sys/kernel/random/boot_id")
	}
	return "boot_id:" + id, nil
}
