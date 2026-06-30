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
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
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

// Startup recovery wiring (HEAL-4 sub-unit 3d-iv-b): run() performs ONE offline
// roll-forward recovery pass before dispatching, on an INITIALIZED workspace. Seed an
// initialized workspace through the real `init` path, append a never-committed
// (intent-only) journal op, then invoke run() again with any command: the startup pass
// must ABANDON that op and reap its journal BEFORE the command runs. The assertion is the
// durable side-effect (the journal worklist is empty afterward), not a return value — the
// recovery.Run call is the unit, and whether the dispatched command itself succeeds is
// irrelevant, so an unknown command is used to prove the pass is independent of any valid
// command body. Recovery emits no envelope of its own (the one-envelope contract,
// DESIGN §3.1): it is a quiet self-heal whose success shows in the resulting state and
// whose failures (Finisher errors leave the journal) surface later via `wi doctor` (§7.5).
//
// Non-vacuity mutant (registered, CMD-MAIN recovery limb): delete the recovery.Run block
// in run() → the seeded intent-only op is never abandoned → journal.Scan still returns it
// → this test RED. The COMPLEMENTARY gate property — recovery must be SKIPPED on an
// uninitialized dir, else recovery.Run's lock.Acquire over a missing .wi/locks errors and
// aborts startup — is guarded by TestRunInitScaffoldsWorkspace above: dropping the
// workspaceInitialized gate reddens it (init never runs; no .wi/ is scaffolded).
func TestRunRecoversAtStartup(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// Initialize the workspace through the real path. (.wi/ does not exist when init runs,
	// so the gate correctly skips recovery for init itself.)
	if code := run(context.Background(), []string{"init"}, io.Discard, io.Discard); code != contract.ExitOK {
		t.Fatalf("init exit = %d, want %d (ExitOK)", code, contract.ExitOK)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	l, err := layout.Resolve(root)
	if err != nil {
		t.Fatalf("layout.Resolve(%s): %v", root, err)
	}

	// A crashed-before-commit op: only an `intent` entry, never `committed`/`done`. The
	// startup recovery pass classifies it Abandoned and discards its journal (no Finisher,
	// so no isolate state is needed to exercise the wiring).
	const opID = "op_intent_only"
	if err := journal.Append(l.JournalDir(), journal.Entry{
		OpID:  opID,
		Kind:  journal.KindIsolateRm,
		Phase: journal.PhaseIntent,
		Task:  "feat",
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	if ops, err := journal.Scan(l.JournalDir()); err != nil || len(ops) != 1 {
		t.Fatalf("precondition: journal.Scan = (%v, %v), want exactly 1 pending op", ops, err)
	}

	// Any startup of the now-initialized workspace runs the recovery pass first.
	var out bytes.Buffer
	_ = run(context.Background(), []string{"frobnicate"}, &out, io.Discard)

	// Durable effect: the never-committed op was abandoned and its journal reaped.
	ops, err := journal.Scan(l.JournalDir())
	if err != nil {
		t.Fatalf("journal.Scan after run: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("journal worklist after startup = %v, want empty (the intent-only op must be abandoned by the startup recovery pass)", ops)
	}
}
