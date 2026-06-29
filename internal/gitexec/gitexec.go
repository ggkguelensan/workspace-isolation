// Package gitexec is the single chokepoint through which wi launches every git
// child process (DESIGN §4, §2.3). It owns the hermetic invocation environment
// and — critically — the no-hidden-network egress belt: Run overlays
// GIT_ALLOW_PROTOCOL=none so git physically refuses every remote transport
// instead of dialing, which is how the "offline commands perform zero network
// dials, including git child processes" invariant (DESIGN §2 #3) is enforced at
// the one place git is ever spawned. RunNetwork is the explicit, narrow opt-in
// for the only online verbs (fetch/clone).
//
// Higher-level typed verbs live in internal/git on top of this; the stderr→kind
// classification table (DESIGN §4) is a separate concern layered over the
// captured Result, so ExitError carries the full Result for it to read.
package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Result captures a finished git invocation. Stdout/Stderr are the raw captured
// streams (callers trim as needed); ExitCode is the process exit status (0 on
// success, the git exit code on failure, -1 if the process never started).
type Result struct {
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExitError reports a git process that ran to completion but exited non-zero.
// The full Result (including stderr) is attached so the stderr→kind classifier
// can map it to a typed contract.ErrorKind without re-running git.
type ExitError struct {
	Result Result
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s",
		strings.Join(e.Result.Args, " "), e.Result.ExitCode, strings.TrimSpace(e.Result.Stderr))
}

// Runner launches git child processes under a controlled base environment.
// Construct with New (inherits the process environment) or NewWithEnv (explicit
// base, used by tests for hermetic isolation).
type Runner struct {
	git  string   // git binary name or path
	base []string // base environment; the belt/prompt vars are overlaid per call
}

// New returns a Runner using the "git" on PATH and the current process
// environment as its base.
func New() *Runner {
	return &Runner{git: "git", base: os.Environ()}
}

// NewWithEnv returns a Runner that runs gitBinary with base as its environment
// (per call, gitexec still overlays its own controlled variables on top).
func NewWithEnv(gitBinary string, base []string) *Runner {
	cp := make([]string, len(base))
	copy(cp, base)
	return &Runner{git: gitBinary, base: cp}
}

// Run executes `git <args>` in dir OFFLINE. It overlays GIT_ALLOW_PROTOCOL=none
// (git refuses every remote transport — the egress belt) and GIT_TERMINAL_PROMPT=0
// (never block on a credential prompt). It returns the captured Result and, on a
// non-zero exit, a non-nil *ExitError wrapping that Result.
func (r *Runner) Run(ctx context.Context, dir string, args ...string) (Result, error) {
	env := setEnv(r.base, "GIT_TERMINAL_PROMPT", "0")
	env = setEnv(env, "GIT_ALLOW_PROTOCOL", "none")
	return r.run(ctx, env, dir, args)
}

// RunNetwork executes `git <args>` in dir permitting network transports. It is
// reserved for the explicitly-online verbs (fetch/clone); GIT_TERMINAL_PROMPT=0
// still applies so a missing credential fails fast rather than hanging.
func (r *Runner) RunNetwork(ctx context.Context, dir string, args ...string) (Result, error) {
	env := setEnv(r.base, "GIT_TERMINAL_PROMPT", "0")
	return r.run(ctx, env, dir, args)
}

func (r *Runner) run(ctx context.Context, env []string, dir string, args []string) (Result, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.git, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	res := Result{
		Args:     append([]string(nil), args...),
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}
	if runErr == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return res, &ExitError{Result: res}
	}
	// The process could not be started (e.g. git not found) or was killed before
	// producing an exit status; ExitCode() is -1 in that case.
	return res, fmt.Errorf("gitexec: run git %s: %w", strings.Join(args, " "), runErr)
}

// setEnv returns env with key set to val, replacing any existing entry (and
// dropping duplicates) so git never sees an ambiguous duplicate key.
func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			if !replaced {
				out = append(out, prefix+val)
				replaced = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, prefix+val)
	}
	return out
}
