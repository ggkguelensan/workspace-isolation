// Package testenv is wi's hermetic real-git test harness (PLAN §M0). Every unit
// that touches the filesystem or shells out to git runs inside an Env: a
// t.TempDir-rooted sandbox with a fully isolated, deterministic git environment.
//
// "Hermetic" means git here ignores the developer's ~/.gitconfig and the system
// config (GIT_CONFIG_GLOBAL=/dev/null + GIT_CONFIG_NOSYSTEM=1), uses a fixed
// author/committer identity AND fixed dates (so commit SHAs are reproducible —
// DESIGN §2 determinism), runs in the C locale (stable English git output for
// stderr→kind parsing), and never prompts or dials the network
// (GIT_TERMINAL_PROMPT=0; all seeding is local file:// work).
//
// RunWI (invoking the built wi binary) is intentionally absent until the CLI
// exists (M3); this harness supplies the git/FS substrate the earlier units need.
package testenv

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// DefaultBranch is the branch seeded origins are initialized on (and wi's
// default base).
const DefaultBranch = "main"

// Fixed identity + dates make every seeded commit byte-identical across runs.
// The date is git's internal "<unix-seconds> <tz>" format.
const (
	authorName  = "wi-test"
	authorEmail = "wi-test@example.invalid"
	fixedDate   = "1767225600 +0000" // 2026-01-01T00:00:00Z
)

// Env is a hermetic, deterministic git sandbox rooted at a temp project dir.
type Env struct {
	Root string   // EvalSymlinks-normalized absolute project root
	env  []string // hermetic environment applied to every git invocation
}

// New returns a fresh Env rooted at a symlink-resolved temp directory. The
// directory is cleaned up automatically by the testing framework.
func New(t *testing.T) *Env {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("testenv: EvalSymlinks temp root: %v", err)
	}
	return &Env{Root: root, env: hermeticEnv()}
}

// hermeticEnv builds the isolated git environment: it drops every ambient GIT_*
// and locale variable, then injects neutralized config paths, a fixed identity
// and dates, the C locale, and a no-prompt setting. PATH/HOME and the rest are
// inherited so git itself is still found and runnable.
func hermeticEnv() []string {
	base := make([]string, 0, len(os.Environ())+12)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "GIT_"):
		case k == "LANG" || k == "LANGUAGE" || strings.HasPrefix(k, "LC_"):
		default:
			base = append(base, kv)
		}
	}
	return append(base,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
		"GIT_AUTHOR_DATE="+fixedDate,
		"GIT_COMMITTER_DATE="+fixedDate,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"LANG=C",
	)
}

// GitEnv returns a copy of the hermetic environment applied to git invocations,
// for tests that drive git through another package (e.g. internal/gitexec) and
// need the same isolation as Git.
func (e *Env) GitEnv() []string {
	out := make([]string, len(e.env))
	copy(out, e.env)
	return out
}

// Git runs `git <args...>` in dir under the hermetic environment, fails the test
// on a non-zero exit (surfacing stderr), and returns trimmed stdout.
func (e *Env) Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = e.env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s (in %s): %v\nstderr: %s", strings.Join(args, " "), dir, err, stderr.String())
	}
	return strings.TrimRight(stdout.String(), "\n")
}

// SeedOrigin creates a bare origin repository named "<name>.git" under the Env
// root, seeded with one deterministic commit on DefaultBranch, and returns its
// absolute path. The work is entirely local — no network.
func (e *Env) SeedOrigin(t *testing.T, name string) string {
	t.Helper()
	scratch := filepath.Join(e.Root, "_seed_"+name)
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatalf("testenv: mkdir scratch: %v", err)
	}
	e.Git(t, scratch, "init", "-b", DefaultBranch)
	if err := os.WriteFile(filepath.Join(scratch, "README.md"), []byte("wi seed\n"), 0o644); err != nil {
		t.Fatalf("testenv: write seed file: %v", err)
	}
	e.Git(t, scratch, "add", "README.md")
	e.Git(t, scratch, "commit", "-m", "seed")

	origin := filepath.Join(e.Root, name+".git")
	e.Git(t, e.Root, "clone", "--bare", scratch, origin)
	return origin
}
