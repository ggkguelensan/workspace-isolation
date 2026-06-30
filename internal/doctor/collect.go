package doctor

import (
	"github.com/ggkguelensan/workspace-isolation/internal/gc"
	"github.com/ggkguelensan/workspace-isolation/internal/journal"
	"github.com/ggkguelensan/workspace-isolation/internal/landstate"
	"github.com/ggkguelensan/workspace-isolation/internal/mirror"
)

// Observations is the complete set of already-gathered, read-only signals a
// doctor run feeds to the detector battery — one field per detector, each the
// exact slice that detector consumes. The `wi doctor` command (a later unit)
// does ALL the I/O: it runs gc.Inspect, loads the mirror Snapshots, Scans the
// journal, Loads the parked-land records, and Inspects each isolate cell, then
// hands the harvested observations here. Keeping the gathering in the command
// and the diagnosis in Collect is what holds DESIGN §2 invariant #3 (no hidden
// network) on this path: Collect dials nothing, reads no disk, and so stays a
// pure, deterministic function the unit tests can exercise without a fixture
// workspace — the same pure-core discipline every individual detector follows.
type Observations struct {
	Orphans     []gc.Candidate       // → DetectOrphans      (markerless worktrees gc cannot prove it owns)
	Drift       []DriftObservation   // → DetectDrift        (three-way isolate-cell drift)
	Mirrors     []mirror.Snapshot    // → DetectMirrorStaleness (base behind origin as of last fetch)
	PendingOps  []journal.OpRecovery // → DetectPendingOps   (unfinished journalled operations)
	ParkedLands []landstate.TaskLand // → DetectParkedLands  (lands not every repo has finished)
}

// Collect is the detector-battery composition seam (DESIGN §7.5): it runs the
// FULL fixed battery over the gathered Observations and returns the union of
// their Findings, which WorstExit then reduces to the run's exit code. It is the
// single registration point the `wi doctor` command calls, so the command never
// learns the detector list — adding the remaining §7.5 detectors (SSOT
// cleanliness, lock inventory, `.wi` parseability, environment probe) is one new
// Observations field plus one append here, and the command is untouched.
//
// Like every detector it is PURE: no I/O, no network, no clock — all gathering is
// the command's job (see Observations). The append order is stable but NOT
// load-bearing: WorstExit ranks findings by severity, not position, and the
// command projects each finding onto the envelope independently.
//
// Guard DOCTOR-BATTERY-COMPLETE: every detector the battery owns must appear in
// this union, so a dropped detector cannot silently stop diagnosing its trouble
// class while the run still exits clean.
func Collect(obs Observations) []Finding {
	var out []Finding
	out = append(out, DetectOrphans(obs.Orphans)...)
	out = append(out, DetectDrift(obs.Drift)...)
	out = append(out, DetectMirrorStaleness(obs.Mirrors)...)
	out = append(out, DetectPendingOps(obs.PendingOps)...)
	out = append(out, DetectParkedLands(obs.ParkedLands)...)
	return out
}
