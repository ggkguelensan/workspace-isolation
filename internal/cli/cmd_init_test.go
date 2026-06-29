package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/config"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// Guard CMD-INIT: the `wi init` handler scaffolds a workspace at the resolved root
// (decision #G — root = cwd, the layout cmd/wi resolves at startup; init takes no
// positional dir operand). It Bootstraps the .wi/ runtime subtree, then writes a
// starter wi.config.jsonc as the COMMIT POINT (written last, O_EXCL). The domain
// mappings this guard pins, which the generic RUN-PIPELINE/DISPATCH-ROUTES guards
// do NOT: a fresh dir → a created Result whose written manifest itself parses under
// config.Load (init's emitter dogfoods the config reader — they cannot drift); a
// re-init over an existing manifest → a *CommandError{kind=already_exists} that
// leaves the existing manifest byte-for-byte intact (the O_EXCL create is the sole
// already-exists guard); and the factory's arg validation (init takes no operands)
// → kind=usage.
//
// Non-vacuity mutant (registered): in initCmd.Run open the manifest with
// O_TRUNC instead of O_EXCL (clobber-on-reinit) → a second init silently rewrites
// the manifest and returns ActionCreated instead of fs.ErrExist→already_exists →
// TestInitOnExistingProjectIsAlreadyExists RED (wrong kind AND the manifest is no
// longer preserved); or make the factory accept any arg count →
// TestInitFactoryRejectsArgs RED.

// unbootstrappedLayout returns a layout over a fresh, EXISTING temp root that has
// NOT been Bootstrapped — the analog of an uninitialized cwd, which is what init
// itself must materialize (it is the unit under test that runs Bootstrap).
func unbootstrappedLayout(t *testing.T) layout.Layout {
	t.Helper()
	l, err := layout.Resolve(t.TempDir())
	if err != nil {
		t.Fatalf("resolve layout: %v", err)
	}
	return l
}

func initFactory(t *testing.T, l layout.Layout) func([]string) (cli.Command, error) {
	t.Helper()
	f, ok := cli.BuildRegistry(cli.Deps{Layout: l})["init"]
	if !ok {
		t.Fatal("BuildRegistry has no \"init\" factory")
	}
	return f
}

func TestInitCreatesProjectScaffold(t *testing.T) {
	l := unbootstrappedLayout(t)

	cmd, err := initFactory(t, l)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil {
		t.Fatal("a successful init must produce a result")
	}
	if res.Action != contract.ActionCreated {
		t.Errorf("Action = %q, want %q", res.Action, contract.ActionCreated)
	}

	// Independent derivation of the manifest path (join the scheme literal over the
	// normalized root, NOT via l.Config()). The written manifest must round-trip
	// through the real config reader — init's emitter dogfoods config.Load.
	configPath := filepath.Join(l.Root(), "wi.config.jsonc")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("manifest not created at %s: %v", configPath, err)
	}
	if _, err := config.Load(configPath); err != nil {
		t.Errorf("init wrote a manifest config.Load rejects: %v", err)
	}

	// Bootstrap ran: the .wi/state runtime subtree exists (path joined independently).
	statePath := filepath.Join(l.Root(), ".wi", "state")
	if fi, err := os.Stat(statePath); err != nil || !fi.IsDir() {
		t.Errorf(".wi/state subtree missing after init (err=%v)", err)
	}
}

func TestInitOnExistingProjectIsAlreadyExists(t *testing.T) {
	l := unbootstrappedLayout(t)

	// First init succeeds and writes the manifest.
	cmd, err := initFactory(t, l)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, err := cmd.Run(context.Background()); err != nil {
		t.Fatalf("first init: %v", err)
	}
	configPath := filepath.Join(l.Root(), "wi.config.jsonc")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	// Re-init must refuse with already_exists and touch nothing.
	cmd2, err := initFactory(t, l)(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := cmd2.Run(context.Background())
	if res != nil {
		t.Errorf("a re-init must not produce a result, got %+v", res)
	}
	var ce *cli.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.CommandError, got %T: %v", err, err)
	}
	if ce.Kind != contract.KindAlreadyExists {
		t.Errorf("Kind = %q, want %q", ce.Kind, contract.KindAlreadyExists)
	}

	// The existing manifest is preserved byte-for-byte (init never clobbers).
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("re-read manifest: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("re-init clobbered the existing manifest:\nbefore=%q\nafter =%q", before, after)
	}
}

func TestInitFactoryRejectsArgs(t *testing.T) {
	l := unbootstrappedLayout(t)
	f := initFactory(t, l)

	for _, args := range [][]string{{"extra"}, {"a", "b"}} {
		if _, err := f(args); !isUsage(err) {
			t.Errorf("args %v: want kind=usage, got %v", args, err)
		}
	}
	// no positional args → a runnable Command.
	cmd, err := f(nil)
	if err != nil || cmd == nil {
		t.Errorf("no args must build a Command, got cmd=%v err=%v", cmd, err)
	}
}
