//go:build unix && !linux && !darwin

package lock

// fsTrustworthy on an unsupported unix conservatively reports NOT trustworthy: wi
// only knows flock(2) reliability for linux and darwin filesystem types, so on any
// other unix it fails closed (auto-break refused) rather than risk breaking a lock
// on a filesystem whose flock semantics it cannot vouch for (DESIGN §7.3). The path
// is unused here; it is accepted to match the linux/darwin signature.
func fsTrustworthy(path string) (bool, error) {
	return false, nil
}
