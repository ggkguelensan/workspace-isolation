package cli_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/config"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/help"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lock"
)

// Guard CMD-REPO-ADD: the `wi repo add <name> <url> [--base <branch>]` handler is a THIN
// seam over config.Add (the AST-preserving edit primitive, guard CONFIG-ADD). The handler's
// OWN responsibilities, which CONFIG-ADD does NOT cover, are:
//   (1) acquire the project-registry lock for the duration — a registry mutation, so it
//       serializes against concurrent `repo add`s; a contended lock → kind=lock_held;
//   (2) parse the command-specific --base/--base= flag (globals were already stripped by
//       Dispatch) and require exactly <name> <url>; an unsafe <name> or wrong arg count →
//       kind=usage (refused in the factory, before any I/O);
//   (3) map config.Add's outcomes onto the return convention — success → Result{created}
//       (no network, no repos[]); ErrDuplicateRepo → already_exists; a missing manifest →
//       not_found+`wi init`.
// It NEVER assembles an envelope or picks an exit code.
//
// Non-vacuity mutant (registered): in repoAddCmd.Run drop the lock.Acquire (never take the
// project-registry lock) → a busy registry no longer refuses → TestRepoAddBusyRegistryIsLockHeld
// RED (want *CommandError{lock_held}, got a created Result). Alternate: drop the
// ErrDuplicateRepo→already_exists branch → a duplicate maps to internal →
// TestRepoAddDuplicateIsAlreadyExists RED (wrong kind).

// addManifest is a small JSONC manifest with a comment + defaults.base + one repo, used to
// prove the handler's edit preserves comments and appends a second repo.
const addManifest = `{
  // repos wi manages
  "defaults": { "base": "main" },
  "repos": [
    { "name": "api", "url": "https://example.com/api.git" }
  ]
}
`

func seedManifest(t *testing.T, l layout.Layout, body string) {
	t.Helper()
	if err := os.WriteFile(l.Config(), []byte(body), 0o644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
}

func repoAddFactory(t *testing.T, l layout.Layout) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l})["repo add"]
	if !ok {
		t.Fatal("BuildRegistry has no \"repo add\" factory")
	}
	return f
}

// A clean add returns a created Result and the manifest re-parses with the new repo, the
// explicit --base recorded, the existing repo and the comment preserved.
func TestRepoAddAppendsToManifest(t *testing.T) {
	l := bootstrappedLayout(t)
	seedManifest(t, l, addManifest)

	cmd, err := repoAddFactory(t, l)([]string{"web", "https://example.com/web.git", "--base", "develop"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(context.Background(), "op_repo_add"))
	if err != nil {
		t.Fatalf("Run: unexpected error %v", err)
	}
	if res == nil || res.Action != contract.ActionCreated {
		t.Fatalf("want a created Result, got %+v", res)
	}
	if len(res.Repos) != 0 {
		t.Errorf("repo add is a registry edit, not a materialization — want no repos[], got %d", len(res.Repos))
	}

	raw, err := os.ReadFile(l.Config())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !contains(string(raw), "// repos wi manages") {
		t.Errorf("comment not preserved by the edit:\n%s", raw)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("rewrite does not re-parse: %v", err)
	}
	if _, ok := cfg.Lookup("api"); !ok {
		t.Errorf("existing repo api was lost by the edit")
	}
	web, ok := cfg.Lookup("web")
	if !ok || web.URL != "https://example.com/web.git" || len(web.Base) != 1 || web.Base[0] != "develop" {
		t.Errorf("web not added correctly: %+v (ok=%v)", web, ok)
	}
}

// Guard REPOADD-STAMP (M4): a successful `repo add` holds the project-registry lock
// for the edit, and must record the operation's holder identity into that lock so the
// self-heal layer can later read WHO is mutating the registry and judge a stale lock's
// liveness (DESIGN §6 / §7.3). The lock file persists after release (Unlock does not
// unlink), so the stamped holder is readable by key once Run returns. The handler
// reads its op_id from the context (cli.OpIDFrom), the same id Execute injects.
//
// Non-vacuity mutant (registered): drop the held.Stamp(OpIDFrom(ctx)) call in
// repoAddCmd.Run → the project-registry lock is acquired but never stamped → its body
// stays empty → lock.ReadHolder returns "empty holder body" → this test RED.
func TestRepoAddStampsHolderOnRegistryLock(t *testing.T) {
	l := bootstrappedLayout(t)
	seedManifest(t, l, addManifest)

	const opID = "op_repo_add_stamp"
	cmd, err := repoAddFactory(t, l)([]string{"web", "https://example.com/web.git"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, err := cmd.Run(cli.WithOpID(context.Background(), opID)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	h, err := lock.ReadHolder(l.LocksDir(), lock.ProjectRegistry())
	if err != nil {
		t.Fatalf("ReadHolder(project-registry lock): %v — repo add did not stamp the holder", err)
	}
	if h.OpID != opID {
		t.Errorf("stamped holder OpID = %q, want %q", h.OpID, opID)
	}
	if h.PID != os.Getpid() {
		t.Errorf("stamped holder PID = %d, want this process %d", h.PID, os.Getpid())
	}
}

// With no --base the inserted repo omits the base field and inherits defaults.base.
func TestRepoAddOmitsInheritedBase(t *testing.T) {
	l := bootstrappedLayout(t)
	seedManifest(t, l, addManifest)

	cmd, err := repoAddFactory(t, l)([]string{"web", "https://example.com/web.git"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, err := cmd.Run(cli.WithOpID(context.Background(), "op_repo_add_nobase")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cfg, err := config.Load(l.Config())
	if err != nil {
		t.Fatalf("re-load: %v", err)
	}
	web, ok := cfg.Lookup("web")
	if !ok || len(web.Base) != 1 || web.Base[0] != "main" { // inherited from defaults.base
		t.Errorf("web base = %v (ok=%v), want inherited [main]", web.Base, ok)
	}
}

// A duplicate name is refused with already_exists, leaving the manifest byte-for-byte intact.
func TestRepoAddDuplicateIsAlreadyExists(t *testing.T) {
	l := bootstrappedLayout(t)
	seedManifest(t, l, addManifest)

	cmd, err := repoAddFactory(t, l)([]string{"api", "https://example.com/other.git"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(context.Background(), "op_repo_add_dup"))
	if res != nil {
		t.Errorf("a duplicate must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindAlreadyExists {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindAlreadyExists)
	}
	raw, _ := os.ReadFile(l.Config())
	if string(raw) != addManifest {
		t.Errorf("manifest must be untouched on a refused duplicate:\n%s", raw)
	}
}

// A busy project-registry refuses with lock_held — proving the handler actually takes the
// lock (this is the registered mutant target: drop lock.Acquire and this reddens).
func TestRepoAddBusyRegistryIsLockHeld(t *testing.T) {
	l := bootstrappedLayout(t)
	seedManifest(t, l, addManifest)

	// Hold the project-registry lock from a separate handle, as a concurrent wi op would.
	held, err := lock.Acquire(l.LocksDir(), lock.ProjectRegistry())
	if err != nil {
		t.Fatalf("pre-acquire registry lock: %v", err)
	}
	defer held.Release()

	cmd, err := repoAddFactory(t, l)([]string{"web", "https://example.com/web.git"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(context.Background(), "op_repo_add_busy"))
	if res != nil {
		t.Errorf("a contended registry must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindLockHeld {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindLockHeld)
	}
	// The manifest must be untouched — the lock blocked the edit before it began.
	raw, _ := os.ReadFile(l.Config())
	if string(raw) != addManifest {
		t.Errorf("manifest must be untouched when the lock is held:\n%s", raw)
	}
}

// A missing manifest is not_found with a `wi init` hint (the same posture as the other
// manifest-reading handlers).
func TestRepoAddMissingManifestIsNotFound(t *testing.T) {
	l := bootstrappedLayout(t) // Bootstrap'd, but no wi.config.jsonc written

	cmd, err := repoAddFactory(t, l)([]string{"web", "https://example.com/web.git"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(cli.WithOpID(context.Background(), "op_repo_add_nomanifest"))
	if res != nil {
		t.Errorf("a missing manifest must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindNotFound {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindNotFound)
	}
	if !contains(ce.Help, "wi init") {
		t.Errorf("missing-manifest help should point at `wi init`, got %q", ce.Help)
	}
}

// TestRepoAddUsageMatchesHelp pins that the wrong-arg-count usage refusal advertises the
// SAME signature `wi help "repo add"` prints — naming BOTH required positionals <name> and
// <url> — by SOURCING the refusal line from internal/help, the SOLE owner of the command
// surface (HELP-REGISTRY-SYNC). This closes the drift that let the help table say
// `wi repo add <url>` while the handler demanded `<name> <url>`: the help usage now IS the
// handler's usage error, so the two surfaces cannot disagree again.
//
// Non-vacuity mutant (registered, documents-required-args limb): revert help.go's "repo add"
// Usage to a form that omits <name> (e.g. back to `wi repo add <url>`) → the handler, now
// sourcing from help, drops <name> from its refusal → the contains("<name>") assertion goes
// RED, while help_test.go (self-referential) and HELP-REGISTRY-SYNC (name sets only) stay
// green — proving this is the one guard that catches the surface lie. Alternate (coupling
// limb): re-hardcode the handler's Message to a literal that diverges from help → the
// equals-help assertion goes RED.
func TestRepoAddUsageMatchesHelp(t *testing.T) {
	l := bootstrappedLayout(t)
	_, err := repoAddFactory(t, l)([]string{"only-name"}) // one positional ⟹ wrong arg count
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("wrong arg count must be a *cli.CommandError, got %T: %v", err, err)
	}
	m, ok := help.For("repo add")
	if !ok {
		t.Fatal(`help.For("repo add") = ok false; the command surface must list it (HELP-REGISTRY-SYNC)`)
	}
	if want := "usage: " + m.Usage; ce.Message != want {
		t.Errorf("usage refusal = %q, want %q (sourced from internal/help, the command-surface SSOT)", ce.Message, want)
	}
	if !contains(ce.Message, "<name>") || !contains(ce.Message, "<url>") {
		t.Errorf("usage refusal %q must name BOTH required positionals <name> and <url>", ce.Message)
	}
}

// The factory validates args BEFORE any I/O: an unsafe repo name and a wrong arg count are
// both kind=usage refusals.
func TestRepoAddFactoryRejectsBadArgs(t *testing.T) {
	l := bootstrappedLayout(t)

	cases := map[string][]string{
		"unsafe name": {"../evil", "https://example.com/x.git"},
		"missing url": {"web"},
		"no args":     {},
		"extra arg":   {"web", "https://example.com/web.git", "extra"},
	}
	for label, args := range cases {
		_, err := repoAddFactory(t, l)(args)
		var ce *cli.CommandError
		if !errors.As(err, &ce) {
			t.Errorf("%s: want *cli.CommandError, got %T: %v", label, err, err)
			continue
		}
		if ce.Kind != contract.KindUsage {
			t.Errorf("%s: Kind = %q, want usage", label, ce.Kind)
		}
	}
}
