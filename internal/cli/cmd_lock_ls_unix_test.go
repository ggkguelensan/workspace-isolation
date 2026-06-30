//go:build unix

package cli_test

import (
	"context"
	"os"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/host"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// Guard CMD-LOCK-LS (M4 self-heal, DESIGN §7.3 / §7.4): the `wi lock ls` handler is the
// CLI surface over lock.List — a READ-ONLY command (Action: read, takes no flock, dials
// no network) that enumerates every lock in the workspace's locks dir and PROJECTS each
// lock.LockStatus onto a contract.LockInfo wire row. The mappings this guard pins, which
// no lower guard covers: the four break-verdict booleans land on the right contract
// fields, the human Reason is carried, and a holder identity rides the nested LockHolder
// EXACTLY when the holder is known — a body-less lock projects a nil holder, never a
// misleading zero-value one. It also pins the factory's no-operand rule (usage).
//
// Non-vacuity mutant (registered): in the LockStatus→LockInfo projection (lockInfoOf),
// drop the `if d.HolderKnown { li.Holder = … }` guard so Holder is left nil for every row
// → the proven-dead row (which HAS a known holder) loses its identity →
// TestLockLsProjectsHolders RED on the non-nil-holder assertion, while the body-less row
// (holder legitimately nil) stays green — isolating the holder projection. Alternate: swap
// two of the four bool fields in the projection → the per-field bool assertions redden.

func lockLsFactory(t *testing.T, l layout.Layout) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l})["lock ls"]
	if !ok {
		t.Fatal(`BuildRegistry has no "lock ls" factory`)
	}
	return f
}

func TestLockLsProjectsHolders(t *testing.T) {
	l := bootstrappedLayout(t)
	locksDir := l.LocksDir()

	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	bootID, err := host.BootID()
	if err != nil {
		t.Fatalf("boot id: %v", err)
	}

	// A proven-dead holder: same host, mismatched boot id → the machine rebooted, so the
	// recorded process is certainly gone. On the trustworthy temp fs this is Safe to break.
	api, _ := lock.Repo("api")
	deadBody, err := lock.Holder{PID: 999999, Host: hostname, BootID: bootID + "-stale", OpID: "op_dead"}.Marshal()
	if err != nil {
		t.Fatalf("marshal holder: %v", err)
	}
	if err := os.WriteFile(api.Path(locksDir), deadBody, 0o600); err != nil {
		t.Fatalf("write api lock: %v", err)
	}
	// A body-less lock → unknown holder, never safe, holder identity omitted.
	reg := lock.ProjectRegistry()
	if err := os.WriteFile(reg.Path(locksDir), nil, 0o600); err != nil {
		t.Fatalf("write registry lock: %v", err)
	}

	cmd, err := lockLsFactory(t, l)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Action != contract.ActionRead {
		t.Errorf("Action = %q, want %q (lock ls is read-only)", res.Action, contract.ActionRead)
	}
	if len(res.Locks) != 2 {
		t.Fatalf("want 2 lock rows, got %d: %+v", len(res.Locks), res.Locks)
	}

	byKey := map[string]contract.LockInfo{}
	for _, li := range res.Locks {
		byKey[li.Key] = li
	}

	dead, ok := byKey["repo:api"]
	if !ok {
		t.Fatalf("missing repo:api row, got keys %v", keysOf(res.Locks))
	}
	if !dead.Safe || !dead.FSTrustworthy || !dead.HolderKnown || !dead.ProvenDead {
		t.Errorf("repo:api verdict = %+v, want all four booleans true (proven-dead holder on trustworthy fs)", dead)
	}
	if dead.Reason == "" {
		t.Error("repo:api row must carry a human Reason diagnostic")
	}
	if dead.Holder == nil {
		t.Fatal("repo:api has a known holder; its nested holder identity must be projected, got nil")
	}
	if dead.Holder.OpID != "op_dead" || dead.Holder.PID != 999999 || dead.Holder.Host != hostname {
		t.Errorf("repo:api holder = %+v, want pid=999999 host=%q op_id=op_dead", dead.Holder, hostname)
	}

	unknown, ok := byKey["project-registry"]
	if !ok {
		t.Fatalf("missing project-registry row, got keys %v", keysOf(res.Locks))
	}
	if unknown.HolderKnown || unknown.Safe {
		t.Errorf("project-registry verdict = %+v, want unknown holder and not safe (body-less lock)", unknown)
	}
	if unknown.Holder != nil {
		t.Errorf("a body-less lock must project a nil holder, got %+v", unknown.Holder)
	}
}

func TestLockLsFactoryRejectsArgs(t *testing.T) {
	l := bootstrappedLayout(t)
	f := lockLsFactory(t, l)

	// `lock ls` takes no operand — any positional arg is a usage refusal.
	for _, args := range [][]string{{"extra"}, {"a", "b"}} {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("args %v: want kind=usage, got %v", args, err)
		}
	}
	// No args → a runnable Command.
	cmd, err := f(nil)
	if err != nil || cmd == nil {
		t.Errorf("no args must build a Command, got cmd=%v err=%v", cmd, err)
	}
}

func keysOf(ls []contract.LockInfo) []string {
	out := make([]string, len(ls))
	for i, li := range ls {
		out[i] = li.Key
	}
	return out
}
