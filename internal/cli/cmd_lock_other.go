//go:build !unix

package cli

// lockCommands returns no commands on non-unix platforms: the lock-self-heal primitives
// (lock.List / AssessBreak / Break) are //go:build unix because they depend on flock(2) +
// statfs filesystem-trust classification, which wi supports only on darwin/linux (its sole
// release targets). This stub keeps BuildRegistry building everywhere the rest of the tree
// does, with the lock subcommands simply absent off-unix (DESIGN §6; ruling in PROGRESS.md).
func lockCommands(d Deps) Registry {
	return nil
}
