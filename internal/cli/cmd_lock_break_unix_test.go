//go:build unix

package cli_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/host"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// Guard CMD-LOCK-BREAK (M4 self-heal, DESIGN §7.3 / HEAL-3): the `wi lock break <key>`
// handler is the ACTION surface over lock.Break — the only command that displaces a lock.
// The mappings this guard pins, which no lower guard covers, are the verdict→envelope
// translation: a SAFE break (holder proven dead on a trustworthy fs) → Action: removed,
// exit 0, with the now-removed lock's verdict carried in the locks block; a REFUSED break
// (live / unknown / untrustworthy holder) → error.kind=lock_held, exit 6, with the SAME
// locks block carried onto the FAILURE envelope so the agent reads WHY the break was
// refused without re-running lock ls. It also pins the factory's one-operand rule (zero
// args or an unparseable key → usage) and the disk effect (a refused break leaves the
// lock file intact — the load-bearing safety property routed through the CLI).
//
// Non-vacuity mutant (registered): in lockBreakCmd.Run, drop the `if !d.Safe { return …
// KindLockHeld … }` branch so every break maps to Action: removed / exit 0 regardless of
// verdict → TestLockBreakLiveHolderRefusesWithLockHeld RED (a live holder exits 0 instead
// of 6 and carries no error), while the proven-dead test stays green. Alternate: drop
// `env.Locks = r.Locks` from envelopeFor's failure arm → the same test reddens on the
// "refusal carries the locks verdict" assertion (the refusal can no longer explain itself).

func lockBreakFactory(t *testing.T, l layout.Layout) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l})["lock break"]
	if !ok {
		t.Fatal(`BuildRegistry has no "lock break" factory`)
	}
	return f
}

func TestLockBreakProvenDeadRemovesAndExitsZero(t *testing.T) {
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

	cmd, err := lockBreakFactory(t, l)([]string{"repo:api"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	var buf bytes.Buffer
	m := cli.Meta{OpID: "op_break", Command: "lock break"}
	code, err := cli.Execute(context.Background(), &buf, m, cli.FormatJSON, cmd)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if code != contract.ExitOK {
		t.Errorf("exit = %d, want %d (safe break succeeds)", code, contract.ExitOK)
	}

	env := decodeOne(t, &buf)
	if !env.OK || env.Error != nil {
		t.Errorf("safe break must be a success envelope, got ok=%v error=%+v", env.OK, env.Error)
	}
	if env.Action != contract.ActionRemoved {
		t.Errorf("Action = %q, want %q (the stale lock was displaced)", env.Action, contract.ActionRemoved)
	}
	if len(env.Locks) != 1 {
		t.Fatalf("want the broken lock's verdict carried in locks[], got %d rows: %+v", len(env.Locks), env.Locks)
	}
	if li := env.Locks[0]; li.Key != "repo:api" || !li.Safe || !li.ProvenDead {
		t.Errorf("locks[0] = %+v, want repo:api Safe+ProvenDead", li)
	}
	if _, err := os.Stat(api.Path(locksDir)); !os.IsNotExist(err) {
		t.Errorf("lock file still present after a safe break (stat err = %v), want removed", err)
	}
}

func TestLockBreakLiveHolderRefusesWithLockHeld(t *testing.T) {
	l := bootstrappedLayout(t)
	locksDir := l.LocksDir()

	// A live holder: CurrentHolder records this very process on this boot, so its flock is
	// held and the holder is demonstrably alive. Breaking it would steal a live lock — the
	// CLI must refuse with lock_held and leave the file intact.
	task1, _ := lock.IsolateState("task1")
	live, err := lock.CurrentHolder("op_live")
	if err != nil {
		t.Fatalf("CurrentHolder: %v", err)
	}
	liveBody, err := live.Marshal()
	if err != nil {
		t.Fatalf("marshal holder: %v", err)
	}
	if err := os.WriteFile(task1.Path(locksDir), liveBody, 0o600); err != nil {
		t.Fatalf("write task1 lock: %v", err)
	}

	cmd, err := lockBreakFactory(t, l)([]string{"isolate-state:task1"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	var buf bytes.Buffer
	m := cli.Meta{OpID: "op_break", Command: "lock break"}
	code, err := cli.Execute(context.Background(), &buf, m, cli.FormatJSON, cmd)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if code != contract.ExitLocked {
		t.Errorf("exit = %d, want %d (lock_held)", code, contract.ExitLocked)
	}

	env := decodeOne(t, &buf)
	if env.OK || env.Error == nil {
		t.Fatalf("a refused break must be a failure envelope, got ok=%v error=%+v", env.OK, env.Error)
	}
	if env.Error.Kind != contract.KindLockHeld {
		t.Errorf("error.kind = %q, want %q", env.Error.Kind, contract.KindLockHeld)
	}
	// The refusal must explain itself: the locks block rides the FAILURE envelope so the
	// agent reads the verdict (not safe, who holds it) without re-running lock ls.
	if len(env.Locks) != 1 {
		t.Fatalf("a lock_held refusal must carry the lock's verdict in locks[], got %d rows: %+v", len(env.Locks), env.Locks)
	}
	if li := env.Locks[0]; li.Key != "isolate-state:task1" || li.Safe {
		t.Errorf("locks[0] = %+v, want isolate-state:task1 not-Safe", li)
	}
	if _, err := os.Stat(task1.Path(locksDir)); err != nil {
		t.Errorf("a refused break removed the lock file (stat err = %v), want intact", err)
	}
}

func TestLockBreakFactoryRejectsBadArgs(t *testing.T) {
	l := bootstrappedLayout(t)
	f := lockBreakFactory(t, l)

	// `lock break` needs exactly one <key>: no operand, too many operands, or an
	// unparseable key are all usage refusals (never a silently-ignored or fabricated key).
	bad := [][]string{nil, {}, {"repo:api", "extra"}, {"not-a-valid-key"}, {""}}
	for _, args := range bad {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("args %v: want kind=usage, got %v", args, err)
		}
	}
	// A well-formed key → a runnable Command.
	cmd, err := f([]string{"repo:api"})
	if err != nil || cmd == nil {
		t.Errorf("a valid key must build a Command, got cmd=%v err=%v", cmd, err)
	}
}
