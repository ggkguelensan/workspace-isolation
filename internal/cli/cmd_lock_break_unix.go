//go:build unix

package cli

import (
	"context"
	"fmt"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// newLockBreakCommand is the `wi lock break <key>` factory: it needs EXACTLY one operand,
// the canonical key of the lock to displace (the same string `lock ls` prints — e.g.
// repo:api, isolate-state:task1, project-registry). No operand, extra operands, or a key
// lock.ParseKey cannot recognize are all usage refusals (kind=usage → exit 64); the handler
// never fabricates a key ParseKey rejected.
func newLockBreakCommand(l layout.Layout, args []string) (Command, error) {
	if len(args) != 1 {
		return nil, &CommandError{Kind: contract.KindUsage, Message: "usage: wi lock break <key>"}
	}
	key, err := lock.ParseKey(args[0])
	if err != nil {
		return nil, &CommandError{Kind: contract.KindUsage, Message: fmt.Sprintf("usage: wi lock break <key>: %v", err)}
	}
	return &lockBreakCmd{layout: l, key: key}, nil
}

// lockBreakCmd is the ACTION counterpart to lock ls (DESIGN §7.3 / HEAL-3): it displaces a
// single stale lock, but ONLY when lock.Break's safe gate authorizes it. lock.Break runs
// the full read-only AssessBreak verdict and unlinks the lock file solely when the holder
// is PROVEN DEAD on a flock-trustworthy fs; otherwise it removes nothing. This handler's
// job is purely to translate that verdict onto the envelope:
//   - Safe (the stale file was removed)  → Action: removed, exit 0, with the now-removed
//     lock's verdict carried in the locks block.
//   - not Safe (live / unknown / untrustworthy holder, file left intact) → a *CommandError
//     with kind=lock_held (exit 6), AND the SAME verdict carried in the locks block so the
//     agent reads WHY the break was refused without re-running lock ls. Returning both a
//     Result and a *CommandError is the documented "failure that still carries detail"
//     path (envelopeFor threads the Result's additive blocks onto the failure envelope).
//
// A genuine I/O / introspection fault from lock.Break is returned unclassified →
// kind=internal: it is an environment failure, not an operator error. It dials no network.
type lockBreakCmd struct {
	layout layout.Layout
	key    lock.Key
}

func (c *lockBreakCmd) Run(ctx context.Context) (*Result, error) {
	d, err := lock.Break(c.layout.LocksDir(), c.key)
	if err != nil {
		return nil, err
	}
	info := lockInfoFrom(c.key.String(), d)
	if !d.Safe {
		return &Result{Action: contract.ActionNoop, Locks: []contract.LockInfo{info}},
			&CommandError{
				Kind:    contract.KindLockHeld,
				Action:  contract.ActionNoop,
				Message: fmt.Sprintf("lock %q is held: %s", c.key.String(), d.Reason),
			}
	}
	return &Result{Action: contract.ActionRemoved, Locks: []contract.LockInfo{info}}, nil
}
