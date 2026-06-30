package cli_test

import (
	"slices"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/help"
)

// Guard HELP-REGISTRY-SYNC: the help command table (internal/help — the SOLE owner of the
// command surface) and the live dispatch registry (BuildRegistry) must describe the SAME
// set of workflow commands, so `wi help` can never lie about which commands exist
// (DESIGN §3.1 / help.go: "help can never lie about the command surface"). The help table
// IS the help text; if a command were added to the registry without a help row (or a help
// row outlived its command) the two surfaces would diverge and this fitness reddens. It is
// the one test that imports BOTH internal/help and the registry — no cycle, because
// internal/help is pure and the registry never imports it (the help→contract projection
// lives in the cli layer).
//
// Decision #HR (recorded PROGRESS.md): `help` is a META-command — a real registered
// command (it backs the advertised help-json capability) that is DELIBERATELY ABSENT from
// the help table, because the overview reads as the init→repo add→sync→isolate new→resolve
// →isolate rm workflow RUNBOOK and `wi help` is not itself a workflow step. So the
// comparison asserts "help" IS in the registry, is NOT a table row, and then compares the
// two surfaces with "help" excluded from the registry keys — the remainder must be EQUAL
// sets.
//
// Non-vacuity mutant (registered): add a bogus key (e.g. "ghost") to BuildRegistry, OR drop
// a row (e.g. "isolate rm") from help.table — either makes the two surfaces diverge and the
// equal-sets assertion goes RED.

func TestHelpTableMatchesRegistry(t *testing.T) {
	// Empty Deps is sufficient: BuildRegistry's KEYS (the command surface) are independent
	// of the deps the factories close over, and this fitness inspects only the key set.
	reg := cli.BuildRegistry(cli.Deps{})

	// `help` is a real command (it backs the help-json capability)...
	if _, ok := reg["help"]; !ok {
		t.Error(`registry must contain the "help" command (it backs the help-json capability)`)
	}
	// ...but is deliberately NOT a row in the workflow help table.
	for _, c := range help.Commands() {
		if c.Name == "help" {
			t.Fatalf(`help.Commands() must NOT list "help": it is a meta-command, absent from the workflow runbook table`)
		}
	}

	// Every OTHER registry command must have exactly one help row, and vice versa.
	var regNames []string
	for name := range reg {
		if name == "help" {
			continue
		}
		regNames = append(regNames, name)
	}
	var helpNames []string
	for _, c := range help.Commands() {
		helpNames = append(helpNames, c.Name)
	}
	slices.Sort(regNames)
	slices.Sort(helpNames)
	if !slices.Equal(regNames, helpNames) {
		t.Errorf("help table and registry describe different command sets:\n  help table = %v\n  registry (minus help) = %v", helpNames, regNames)
	}
}
