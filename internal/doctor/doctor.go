// Package doctor is the read-only health-diagnosis domain behind `wi doctor`
// (alias `wi check`), the last HEAL layer (HEAL-8, DESIGN §7.5). doctor never
// mutates the workspace and never touches the network: it runs a fixed battery
// of detectors, each reducing one class of trouble to a Finding, and reports the
// collected Findings in the frozen envelope. `--fix` (a later unit) re-dispatches
// each fixable Finding to its owning SAFE-tier heal (isolate repair, gc) and
// re-runs detection between every heal — but the diagnosis itself is pure read.
//
// This unit owns the seam every detector and the command share: the Finding model
// and the run's overall exit code. A detector's job is to emit Findings; the
// command's job is to project them onto the envelope and exit with WorstExit's
// verdict. Neither hand-codes an exit number — WorstExit is the single authority
// for "Exit = the worst finding's code" (§7.5), layered on exitcontract's §3.2
// matrix so doctor inherits the exact kind↔exit pairing the wire contract pins.
package doctor

import (
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
	"github.com/ggkguelensan/workspace-isolation/internal/exitcontract"
)

// Severity splits a Finding into the two ways doctor treats trouble. The split is
// the whole reason a doctor run can succeed while still reporting problems: only
// ERROR findings move the exit code; WARNINGs are surfaced for the operator but
// are exit-neutral.
type Severity string

const (
	// SeverityError contributes its Kind's §3.2 exit code to the run's verdict.
	// A run with any error finding fails with the worst error's code.
	SeverityError Severity = "error"
	// SeverityWarning is exit-neutral: surfaced in the envelope but it never
	// raises the exit code. DESIGN §7.5 pins the keystone case — mirror staleness
	// is a WARNING here and must NEVER make `wi doctor` exit 6 (only the land path
	// refuses on a stale mirror); a warnings-only diagnosis is a clean exit 0.
	SeverityWarning Severity = "warning"
)

// Finding is one detector's verdict on one observed condition — the unit of a
// doctor diagnosis. A detector emits zero or more; the command projects each onto
// the envelope (Kind/Code/Message/Repo ride into an Error or a per-repo result)
// and feeds the ERROR ones to WorstExit.
//
// Kind is a frozen contract.ErrorKind so doctor speaks the same closed vocabulary
// every other command does (an agent branches on Kind + exit code, never on Code
// or Message). Code is the stable machine sub-code that distinguishes findings
// sharing a Kind (e.g. ssot_dirty vs ssot_stray_branch both map to a refusal
// Kind) — it is doctor's diagnostic granularity, finer than the exit bucket.
type Finding struct {
	Detector string             // which detector produced it (e.g. "ssot", "orphans", "locks", "journal", "mirror")
	Kind     contract.ErrorKind // the frozen error.kind this maps to; its §3.2 exit code feeds WorstExit
	Code     string             // stable machine sub-code (ssot_dirty, orphan_unexplained, fs_unsafe_for_locks, …)
	Severity Severity           // ERROR (moves the exit code) or WARNING (exit-neutral)
	Message  string             // human-readable detail for the operator
	Repo     string             // optional cell identity (the repo the finding is about)
	Task     string             // optional cell identity (the isolate task the finding is about)
}

// severityOrder ranks the exit codes a doctor run can carry from WORST (index 0)
// to least severe, defining "worst finding's code" (§7.5) as a SEVERITY rank, NOT
// the numeric exit value. Decision #DOCTOR-EXIT-WORST: exit numbers are coarse
// buckets, not a severity scale — usage(64)/internal(70) carry the largest
// numbers, yet a contended lock or an unhealable conflict is a worse *health*
// verdict than a malformed flag, and a corrupt `.wi` state is worse still. The
// rank reflects how fundamentally the workspace cannot make safe automated
// progress:
//
//   - internal (70): corrupt/unparseable `.wi` state — wi cannot trust its own
//     bookkeeping, so nothing else is reliable. WORST.
//   - lock_held (6): contended locks / a filesystem unsafe for locks — operations
//     are blocked and the safe-tier heal cannot break them.
//   - needs_approval (5): a gated action awaits the operator (reachable in v2).
//   - refused (4): dirty work / conflict / an unexplained orphan blocks a heal —
//     serious but localized, and the operator can resolve the named cell.
//   - not_found (3): something expected is missing.
//   - usage (64): a malformed invocation — about how doctor was CALLED, not
//     workspace health, so it ranks below genuine workspace troubles.
//   - partial (2): a known, resumable pending op — the mildest non-ok; the
//     workspace is sound, an op just needs draining.
//   - interrupted (130): only from a signal, never a detector finding; ranked
//     just above clean for totality.
//   - ok (0): clean — the absence of any error finding.
var severityOrder = []contract.ExitCode{
	contract.ExitInternal,    // 70
	contract.ExitLocked,      // 6
	contract.ExitNeedsApprov, // 5
	contract.ExitRefused,     // 4
	contract.ExitNotFound,    // 3
	contract.ExitUsage,       // 64
	contract.ExitPartial,     // 2
	contract.ExitInterrupted, // 130
	contract.ExitOK,          // 0
}

// rankOf returns code's position in the severity precedence (lower = worse). An
// exit code absent from the table fails SAFE-LOUD: it ranks below index 0, i.e.
// MORE severe than any known code, so an unrecognized verdict can never be
// silently treated as mild and slip past the exit. In a correct build every code
// WorstExit can produce (via exitcontract over the closed kind set) is listed, so
// this default is unreachable.
func rankOf(code contract.ExitCode) int {
	for i, c := range severityOrder {
		if c == code {
			return i
		}
	}
	return -1
}

// moreSevere reports whether exit code a is a worse diagnosis than b under the
// #DOCTOR-EXIT-WORST precedence (NOT numeric comparison).
func moreSevere(a, b contract.ExitCode) bool {
	return rankOf(a) < rankOf(b)
}

// WorstExit computes a completed doctor run's process exit code: the rule
// "Exit = the worst finding's code" (DESIGN §7.5). WARNING findings are
// exit-neutral — skipped entirely — so a run with no ERROR findings (clean, or
// warnings only) exits 0. Among ERROR findings it returns the most severe one's
// §3.2 exit code, where severity follows the #DOCTOR-EXIT-WORST precedence rather
// than the numeric code value (so e.g. a lock_held error outranks a usage error
// even though 64 > 6). Each finding's code comes from exitcontract.ExitCodeFor,
// the single kind↔exit authority, so doctor adds an ORDERING over codes without
// duplicating the matrix.
func WorstExit(findings []Finding) contract.ExitCode {
	worst := contract.ExitOK
	haveWorst := false
	for _, f := range findings {
		if f.Severity != SeverityError {
			continue // WARNINGs (and any non-error severity) are exit-neutral — §7.5
		}
		code := exitcontract.ExitCodeFor(f.Kind)
		if !haveWorst || moreSevere(code, worst) {
			worst = code
			haveWorst = true
		}
	}
	return worst
}
