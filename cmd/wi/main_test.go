package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// Guard CMD-MAIN: cmd/wi is the single process entry — the one main package and the one
// os.Exit site (via exitcontract.Exit). main() itself is untestable (it terminates the
// process), so the wiring lives in run(ctx, args, stdout, stderr) → contract.ExitCode,
// which main() does nothing but call and hand to exitcontract.Exit. run's job is the
// production wiring the generic pipeline can't prove on its own: discover the root from
// cwd (decision #G), build the REAL Deps (layout + the os/exec git driver + System clock)
// and BuildRegistry over them, then Dispatch — emitting EXACTLY ONE envelope and returning
// the code Dispatch computed. These guards pin the two facts unit tests below the binary
// cannot: (1) the happy path reaches a REAL handler and mutates the filesystem (a stub
// registry or a misresolved root would not create .wi/); (2) run PROPAGATES Dispatch's
// non-zero exit code rather than swallowing it.
//
// Non-vacuity mutant (registered): in run, return contract.ExitOK instead of the code
// Dispatch returned → TestRunUnknownCommandExitsUsage RED (got 0, want 64). Alternate:
// hand Dispatch an empty Registry{} instead of BuildRegistry(deps) → every command is
// unknown → TestRunInitScaffoldsWorkspace RED (no .wi/, ok:false/usage not created).

// decodeEnvelope asserts out holds EXACTLY ONE compact JSON line and returns its decoded
// shape (only the fields these guards assert on, so the test doesn't depend on the
// contract's custom (un)marshaling).
func decodeEnvelope(t *testing.T, out []byte) struct {
	OK     bool   `json:"ok"`
	Action string `json:"action"`
	Error  *struct {
		Kind string `json:"kind"`
	} `json:"error"`
} {
	t.Helper()
	if n := bytes.Count(out, []byte{'\n'}); n != 1 {
		t.Fatalf("want EXACTLY one envelope line (one newline), got %d in: %q", n, out)
	}
	var env struct {
		OK     bool   `json:"ok"`
		Action string `json:"action"`
		Error  *struct {
			Kind string `json:"kind"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &env); err != nil {
		t.Fatalf("envelope is not valid JSON (%v): %q", err, out)
	}
	return env
}

// Happy path: `wi init` in an empty dir reaches the real init handler, emits one created
// success envelope, exits 0, and actually scaffolds .wi/ on disk — proving run wired the
// real registry over a root resolved from cwd, not a stub.
func TestRunInitScaffoldsWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var out bytes.Buffer
	code := run(context.Background(), []string{"init"}, &out, io.Discard)

	if code != contract.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK)", code, contract.ExitOK)
	}
	env := decodeEnvelope(t, out.Bytes())
	if !env.OK || env.Action != "created" {
		t.Errorf("envelope = {ok:%v action:%q}, want {ok:true action:created}", env.OK, env.Action)
	}
	// The real handler ran: cwd is now a wi workspace. Resolve symlinks the way
	// layout.Resolve does so the assertion matches where init actually wrote.
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".wi")); err != nil {
		t.Errorf("init did not scaffold .wi/ under the resolved root: %v", err)
	}
}

// Exit-code propagation: an unknown command is a usage refusal (exit 64). run must return
// that code, not swallow it to 0 — the mutant that returns ExitOK reddens here.
func TestRunUnknownCommandExitsUsage(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var out bytes.Buffer
	code := run(context.Background(), []string{"frobnicate"}, &out, io.Discard)

	if code != contract.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", code, contract.ExitUsage)
	}
	env := decodeEnvelope(t, out.Bytes())
	if env.OK {
		t.Errorf("unknown command must be ok:false, got ok:true")
	}
	if env.Error == nil || env.Error.Kind != "usage" {
		t.Errorf("unknown command error = %+v, want kind=usage", env.Error)
	}
}
