package cli_test

import (
	"reflect"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// Guard SHAPE-ASSEMBLE: the Success/Failure constructors are the SOLE place an
// envelope's spine is built, and they enforce the two couplings callers must never
// get wrong — ok ⟺ error==nil, and the always-identical common fields
// (schema_version == contract.SchemaVersion, capabilities == contract.Capabilities()).
// A handler returns domain data; it never hand-sets these.
//
// Non-vacuity mutant (registered): in Success set e.OK=false (or leave Error non-nil)
// => TestSuccessEnvelopeCoupling RED; or have the shared spine omit Capabilities
// (leave it nil) => the capabilities assertion in BOTH coupling tests RED.
func TestSuccessEnvelopeCoupling(t *testing.T) {
	m := cli.Meta{OpID: "op_x", Command: "isolate new", DryRun: false}
	env := cli.Success(m, contract.ActionCreated, nil)

	if !env.OK {
		t.Error("Success envelope must have ok=true")
	}
	if env.Error != nil {
		t.Errorf("Success envelope must have nil error, got %+v", env.Error)
	}
	if env.Action != contract.ActionCreated {
		t.Errorf("Action = %q, want %q", env.Action, contract.ActionCreated)
	}
	assertCommonFields(t, env, m)
}

func TestFailureEnvelopeCoupling(t *testing.T) {
	m := cli.Meta{OpID: "op_y", Command: "repo add", DryRun: false}
	env := cli.Failure(m, contract.ActionNoop, contract.Error{
		Kind:    contract.KindAlreadyExists,
		Message: "repo already declared",
	})

	if env.OK {
		t.Error("Failure envelope must have ok=false")
	}
	if env.Error == nil {
		t.Fatal("Failure envelope must carry a non-nil error")
	}
	if env.Error.Kind != contract.KindAlreadyExists {
		t.Errorf("Error.Kind = %q, want %q", env.Error.Kind, contract.KindAlreadyExists)
	}
	if env.Action != contract.ActionNoop {
		t.Errorf("Action = %q, want %q", env.Action, contract.ActionNoop)
	}
	assertCommonFields(t, env, m)
}

func assertCommonFields(t *testing.T, env contract.Envelope, m cli.Meta) {
	t.Helper()
	if env.SchemaVersion != contract.SchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", env.SchemaVersion, contract.SchemaVersion)
	}
	if !reflect.DeepEqual(env.Capabilities, contract.Capabilities()) {
		t.Errorf("Capabilities = %v, want %v", env.Capabilities, contract.Capabilities())
	}
	if env.OpID != m.OpID {
		t.Errorf("OpID = %q, want %q", env.OpID, m.OpID)
	}
	if env.Command != m.Command {
		t.Errorf("Command = %q, want %q", env.Command, m.Command)
	}
	if env.DryRun != m.DryRun {
		t.Errorf("DryRun = %v, want %v", env.DryRun, m.DryRun)
	}
}

// Guard SHAPE-DRYRUN-EXIT0: ExitFor derives the process code from the assembled
// envelope as a PURE function of the top-level error — a clean envelope is exit 0,
// an error maps through the §3.2 failure matrix (incl. partial→2), and a populated
// blocked[] (a would-block dry-run verdict) is EXIT-NEUTRAL: it never makes the exit
// non-zero. DESIGN §3.2: "every --dry-run → exit 0" means a dry-run that RAN puts its
// verdicts in blocked[] (error stays nil → 0); it does NOT mean dry-run swallows a
// genuine top-level error (a usage error on a --dry-run invocation still exits 64,
// decision #D).
//
// Non-vacuity mutant (registered): make ExitFor return a refusal code when
// len(env.Blocked) > 0 (treat a would-block as a refusal) => the dry-run-with-blocked
// case RED. A blanket `if env.DryRun { return ExitOK }` is the OVER-correction the
// usage-on-dry-run assertion guards against (it would wrongly return 0).
func TestExitForCouplesErrorToCode(t *testing.T) {
	m := cli.Meta{OpID: "op", Command: "isolate new"}

	if got := cli.ExitFor(cli.Success(m, contract.ActionCreated, nil)); got != contract.ExitOK {
		t.Errorf("ExitFor(success) = %d, want %d", got, contract.ExitOK)
	}

	for kind, want := range map[contract.ErrorKind]contract.ExitCode{
		contract.KindLockHeld:      contract.ExitLocked,
		contract.KindPartial:       contract.ExitPartial,
		contract.KindNotFound:      contract.ExitNotFound,
		contract.KindAlreadyExists: contract.ExitRefused,
		contract.KindUsage:         contract.ExitUsage,
	} {
		env := cli.Failure(m, contract.ActionNoop, contract.Error{Kind: kind, Message: "x"})
		if got := cli.ExitFor(env); got != want {
			t.Errorf("ExitFor(error kind %q) = %d, want %d", kind, got, want)
		}
	}
}

func TestExitForBlockedVerdictsAreExitNeutral(t *testing.T) {
	dry := cli.Meta{OpID: "op", Command: "isolate new", DryRun: true}

	// A dry-run that planned and found a would-block verdict: blocked[] populated,
	// no top-level error -> still exit 0.
	planned := cli.Success(dry, contract.ActionNoop, nil)
	planned.Blocked = []contract.BlockItem{
		{Repo: "api", Kind: contract.KindDirtyWorktree, Reason: "would refuse: dirty worktree"},
	}
	if got := cli.ExitFor(planned); got != contract.ExitOK {
		t.Errorf("ExitFor(dry-run with blocked verdict) = %d, want %d (blocked must be exit-neutral)", got, contract.ExitOK)
	}

	// Honesty check (decision #D): a genuine usage error on a --dry-run invocation is
	// NOT swallowed to 0 — the command never produced a dry-run plan.
	usage := cli.Failure(dry, contract.ActionNoop, contract.Error{Kind: contract.KindUsage, Message: "bad flag"})
	if got := cli.ExitFor(usage); got != contract.ExitUsage {
		t.Errorf("ExitFor(usage error on dry-run) = %d, want %d (dry-run must not swallow a real error)", got, contract.ExitUsage)
	}
}
