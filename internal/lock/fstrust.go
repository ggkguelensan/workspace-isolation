package lock

// FSTrustworthy reports whether the filesystem backing path is one on which flock(2)
// is known to be locally reliable — the fs-trust gate of lock self-heal (DESIGN
// §7.3). wi REFUSES to auto-break a stale lock unless this returns true, because
// flock's mutual-exclusion guarantee does not hold across hosts on a network
// filesystem (NFS/SMB/9p/AFP/…): a "dead" LOCAL pid says nothing about a process on
// another machine that may hold the same flock, so breaking there would corrupt
// mutual exclusion.
//
// The per-OS classifier is an allowlist of known-local filesystem types. Anything
// unrecognized — a network fs, a novel local fs, an unsupported platform — is
// conservatively NOT trustworthy, so the failure mode is "refuse to break" (the lock
// stands; lock_held / exit 6, and the operator resolves it), never "wrongly break".
// This composes with ProvenDead: a break is authorized only when the fs is
// trustworthy AND the holder is proven dead.
func FSTrustworthy(path string) (bool, error) {
	return fsTrustworthy(path)
}
