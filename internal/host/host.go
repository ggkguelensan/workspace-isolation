// Package host derives stable facts about the machine and the current boot that
// wi stamps into lock-holder identity — the {pid, host, boot_id, op_id} body of a
// lock file (DESIGN §6 / §7.3). The boot_id is what makes a recorded holder pid
// reuse-safe across reboots: a lock whose boot_id differs from the current boot
// was necessarily written before a reboot, so its pid belongs to a dead boot and
// the holder is provably gone — the lock-liveness layer can break it without ever
// probing a live peer.
//
// Derivation is platform-specific and dependency-free (open decision #3, and #6
// "zero new deps"): see bootid_linux.go and bootid_darwin.go. Supported platforms
// are linux and darwin (the CI matrix); other OSes are out of scope until M5's
// portability matrix formalizes them.
package host

// BootID returns a per-boot identifier that is STABLE for the lifetime of the
// current boot and DIFFERS after a reboot. The value is opaque: callers compare
// it for equality (current boot vs. the boot recorded in a lock body) and never
// parse it. Sleep/wake does not change it. It carries a platform-tagged prefix
// (e.g. "boot_id:" on linux, "boottime:" on darwin) so two identifiers can never
// be confused across platforms even if their tails coincide.
func BootID() (string, error) {
	return bootID()
}
