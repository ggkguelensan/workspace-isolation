package gitexec_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
	"github.com/ggkguelensan/workspace-isolation/internal/testenv"
)

// Guard GITEXEC-OFFLINE-BELT (the unit-level half of INV-NO-NETWORK, DESIGN §2.3)
// and GITEXEC-CAPTURE.
//
// internal/gitexec is the single chokepoint through which every git child
// process is launched. The no-hidden-network invariant is physically enforced
// here: Run overlays GIT_ALLOW_PROTOCOL=none, so git refuses EVERY remote
// transport (the whitelist is empty) instead of dialing. RunNetwork is the
// explicit, narrow exception for the online verbs (fetch/clone).
//
// The proof is two-sided and fully hermetic (a local file:// remote, no real
// network): the same ls-remote that SUCCEEDS through RunNetwork is REFUSED
// through Run with "transport 'file' not allowed" — so the refusal is
// attributable to the belt, not to a broken remote.
//
// Non-vacuity:
//   - GITEXEC-OFFLINE-BELT: drop GIT_ALLOW_PROTOCOL=none from Run's overlay →
//     offline ls-remote succeeds → TestOfflineRefusesTransport RED.
//   - GITEXEC-CAPTURE: make Run ignore the child exit code (always nil error) →
//     TestRunSurfacesExitError RED.

func TestOfflineRefusesTransport(t *testing.T) {
	env := testenv.New(t)
	origin := env.SeedOrigin(t, "acme")
	fileURL := "file://" + origin
	r := gitexec.NewWithEnv("git", env.GitEnv())

	// Offline: the egress belt must refuse the transport, before any dial.
	res, err := r.Run(context.Background(), env.Root, "ls-remote", fileURL)
	var ee *gitexec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("offline ls-remote err = %v (%T), want *gitexec.ExitError", err, err)
	}
	if res.ExitCode == 0 {
		t.Errorf("offline ls-remote exit = 0, want non-zero (belt should refuse)")
	}
	if !strings.Contains(res.Stderr, "not allowed") {
		t.Errorf("offline stderr = %q, want it to mention the transport is not allowed", res.Stderr)
	}

	// Online: the identical command must succeed, proving the offline refusal is
	// the belt and not a broken remote (two-sided non-vacuity).
	res2, err2 := r.RunNetwork(context.Background(), env.Root, "ls-remote", fileURL)
	if err2 != nil {
		t.Fatalf("online ls-remote err = %v, want success", err2)
	}
	if res2.ExitCode != 0 {
		t.Errorf("online ls-remote exit = %d, want 0", res2.ExitCode)
	}
	if !strings.Contains(res2.Stdout, "refs/heads/"+testenv.DefaultBranch) {
		t.Errorf("online stdout = %q, want it to list refs/heads/%s", res2.Stdout, testenv.DefaultBranch)
	}
}

func TestRunCapturesStdout(t *testing.T) {
	env := testenv.New(t)
	r := gitexec.NewWithEnv("git", env.GitEnv())

	res, err := r.Run(context.Background(), env.Root, "version")
	if err != nil {
		t.Fatalf("git version: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", res.ExitCode)
	}
	if !strings.HasPrefix(res.Stdout, "git version") {
		t.Errorf("stdout = %q, want it to start with %q", res.Stdout, "git version")
	}
}

func TestRunSurfacesExitError(t *testing.T) {
	env := testenv.New(t)
	r := gitexec.NewWithEnv("git", env.GitEnv())

	res, err := r.Run(context.Background(), env.Root, "definitely-not-a-git-command")
	var ee *gitexec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v (%T), want *gitexec.ExitError", err, err)
	}
	if res.ExitCode == 0 {
		t.Errorf("exit = 0, want non-zero for an unknown subcommand")
	}
	if res.Stderr == "" {
		t.Errorf("stderr is empty, want git's diagnostic captured")
	}
	// The ExitError must carry the Result so the later stderr→kind classifier can
	// read it.
	if ee.Result.ExitCode != res.ExitCode {
		t.Errorf("ExitError.Result.ExitCode = %d, want %d", ee.Result.ExitCode, res.ExitCode)
	}
}
