//go:build unix

package cli

import (
	"context"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// lockCommands returns the lock-self-heal subcommands, which exist only on unix: their
// domain primitives (lock.List / AssessBreak / Break) gate on flock(2) + statfs filesystem
// trust and are //go:build unix, so the binary carries them on darwin/linux (wi's only
// release targets) and BuildRegistry merges them in there. The sibling cmd_lock_other.go
// stub contributes none on a non-unix build (DESIGN §6 portability; ruling in PROGRESS.md).
// `lock ls` is the read-only inventory; `lock break` is the action that displaces a stale
// lock when (and only when) doing so is provably safe.
func lockCommands(d Deps) Registry {
	return Registry{
		"lock ls":    func(args []string) (Command, error) { return newLockLsCommand(d.Layout, args) },
		"lock break": func(args []string) (Command, error) { return newLockBreakCommand(d.Layout, args) },
	}
}

// newLockLsCommand is the `wi lock ls` factory: the command lists every lock in the
// workspace and takes NO positional args, so any operand is a clean usage refusal
// (kind=usage → exit 64) rather than a silently-ignored argument.
func newLockLsCommand(l layout.Layout, args []string) (Command, error) {
	if len(args) != 0 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi lock ls"}
	}
	return &lockLsCmd{layout: l}, nil
}

// lockLsCmd answers "which locks exist, and is each safe to break?" as a READ-ONLY
// projection (DESIGN §7.3 / §7.4): it enumerates the locks dir via lock.List — which takes
// no flock and only statfs's the dir + reads each body, exactly how a contender inspects a
// lock it does not hold — and projects every LockStatus onto a contract.LockInfo wire row.
// It mutates nothing and dials no network. A genuine I/O fault from List (readdir / statfs
// / host introspection) is returned unclassified → kind=internal: it is an environment
// failure, not an operator error. A missing locks dir is not an error (List yields none).
type lockLsCmd struct {
	layout layout.Layout
}

func (c *lockLsCmd) Run(ctx context.Context) (*Result, error) {
	statuses, err := lock.List(c.layout.LocksDir())
	if err != nil {
		return nil, err
	}
	infos := make([]contract.LockInfo, 0, len(statuses))
	for _, s := range statuses {
		infos = append(infos, lockInfoOf(s))
	}
	return &Result{Action: contract.ActionRead, Locks: infos}, nil
}

// lockInfoOf projects one lock.LockStatus (the read-only inventory shape) onto its
// contract.LockInfo wire row by delegating to lockInfoFrom over its key + decision.
func lockInfoOf(s lock.LockStatus) contract.LockInfo {
	return lockInfoFrom(s.Key.String(), s.Decision)
}

// lockInfoFrom projects a (key, break-verdict) pair onto its contract.LockInfo wire row:
// the four break-verdict booleans map across verbatim, Reason carries the human
// diagnostic, and the holder identity is attached as a nested LockHolder EXACTLY when the
// holder is known. A body-less / unparseable lock (HolderKnown=false) projects with a nil
// holder, never a misleading zero-value one — it is conservatively never breakable, and the
// omitted holder is the wire signal of that. Both `lock ls` (over a LockStatus) and `lock
// break` (over Break's returned BreakDecision) share this one projection so the verdict an
// agent reads is identical whether it inspected the lock or acted on it.
func lockInfoFrom(key string, d lock.BreakDecision) contract.LockInfo {
	li := contract.LockInfo{
		Key:           key,
		Safe:          d.Safe,
		FSTrustworthy: d.FSTrustworthy,
		HolderKnown:   d.HolderKnown,
		ProvenDead:    d.ProvenDead,
		Reason:        d.Reason,
	}
	if d.HolderKnown {
		li.Holder = &contract.LockHolder{
			PID:    d.Holder.PID,
			Host:   d.Holder.Host,
			BootID: d.Holder.BootID,
			OpID:   d.Holder.OpID,
		}
	}
	return li
}
