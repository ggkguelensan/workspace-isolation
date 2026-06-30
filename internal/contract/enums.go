// Package contract is the SOLE owner of wi's wire contract: the closed enums,
// exit codes, capability vocabulary, and (in later units) the Envelope wire
// type. Every other package imports these constants; none redefine them.
//
// See DESIGN.md §3 for the locked specification.
package contract

// SchemaVersion is the wire-contract version embedded in every envelope.
// Bump (minor for additive, major for breaking) on any envelope shape change;
// the contract.lock.json drift guard fails CI if the shape moves without it.
//
// 1.1 (M4, additive): folded in the `locks` block (the `wi lock ls` inventory) —
// a new optional, omitempty block; v0 output is unchanged (the block is nil on
// every M0–M3 command), so consumers pinned to 1.0 shapes keep parsing.
const SchemaVersion = "1.1"

// Action is the closed set of mutation outcomes reported in Envelope.Action.
type Action string

const (
	ActionCreated Action = "created"
	ActionRemoved Action = "removed"
	ActionSynced  Action = "synced"
	ActionLanded  Action = "landed"
	ActionRead    Action = "read"
	ActionNoop    Action = "noop"
)

// AllActions returns the closed Action vocabulary in canonical order.
func AllActions() []Action {
	return []Action{
		ActionCreated, ActionRemoved, ActionSynced,
		ActionLanded, ActionRead, ActionNoop,
	}
}

// ErrorKind is the closed set of machine-branchable error categories.
// Agents branch on ErrorKind and ExitCode, NEVER on Error.Message text.
type ErrorKind string

const (
	KindUsage         ErrorKind = "usage"
	KindNotFound      ErrorKind = "not_found"
	KindDirtyWorktree ErrorKind = "dirty_worktree"
	KindConflict      ErrorKind = "conflict"
	KindLockHeld      ErrorKind = "lock_held"
	KindMirrorStale   ErrorKind = "mirror_stale"
	KindNeedsApproval ErrorKind = "needs_approval"
	KindAlreadyExists ErrorKind = "already_exists"
	KindPartial       ErrorKind = "partial"
	KindRemoteError   ErrorKind = "remote_error"
	KindInternal      ErrorKind = "internal"
)

// AllErrorKinds returns the closed ErrorKind vocabulary in canonical order.
func AllErrorKinds() []ErrorKind {
	return []ErrorKind{
		KindUsage, KindNotFound, KindDirtyWorktree, KindConflict,
		KindLockHeld, KindMirrorStale, KindNeedsApproval, KindAlreadyExists,
		KindPartial, KindRemoteError, KindInternal,
	}
}

// WarningCode is the closed vocabulary for the non-fatal Warning.Code field.
// Warnings are advisory notes an agent MAY surface but never branches control
// flow on (that is error.kind's job). The set is closed and double-entry-guarded;
// growing it is a deliberate, schema-bumped change.
//
// v0 set (open decision #1, ruled 2026-06-29): only codes a wired M0–M3 command
// can emit and that are knowable offline. Staleness is NOT here — it is surfaced
// by the structured mirror_freshness.stale field (DESIGN §3.3), not a warning.
type WarningCode string

const (
	// WarnHydrateSkipped: isolate-new declined to hydrate a gitignored file
	// because it was not on the allowlist or matched a hard-deny pattern.
	WarnHydrateSkipped WarningCode = "hydrate_skipped"
	// WarnBaseBehindSSOT: the isolate's base is behind the current SSOT base
	// ref; landing would require a sync first. Informational on read paths.
	WarnBaseBehindSSOT WarningCode = "base_behind_ssot"
)

// AllWarningCodes returns the closed WarningCode vocabulary in canonical order.
func AllWarningCodes() []WarningCode {
	return []WarningCode{
		WarnHydrateSkipped, WarnBaseBehindSSOT,
	}
}

// ExitCode is the closed set of process exit codes. See DESIGN.md §3.2.
type ExitCode int

const (
	ExitOK          ExitCode = 0   // success — including noop and every --dry-run
	ExitPartial     ExitCode = 2   // durable, resumable partial success
	ExitNotFound    ExitCode = 3   // not_found
	ExitRefused     ExitCode = 4   // dirty_worktree / conflict / already_exists
	ExitNeedsApprov ExitCode = 5   // needs_approval (reachable once hooks ship, v2)
	ExitLocked      ExitCode = 6   // lock_held, or mirror_stale on the land path
	ExitUsage       ExitCode = 64  // usage error
	ExitInternal    ExitCode = 70  // internal error
	ExitInterrupted ExitCode = 130 // interrupted (SIGINT)
)

// AllExitCodes returns the closed ExitCode set in canonical (ascending) order.
func AllExitCodes() []ExitCode {
	return []ExitCode{
		ExitOK, ExitPartial, ExitNotFound, ExitRefused, ExitNeedsApprov,
		ExitLocked, ExitUsage, ExitInternal, ExitInterrupted,
	}
}

// Capability is the closed vocabulary advertised in Envelope.Capabilities.
// Capabilities advertises ONLY wired commands — see Capabilities().
type Capability string

const (
	CapHelpJSON        Capability = "help-json"
	CapResolveBlock    Capability = "resolve-block"
	CapDryRun          Capability = "dry-run"
	CapPartialSuccess  Capability = "partial-success"
	CapLand            Capability = "land"
	CapLandAtomic      Capability = "land-atomic"
	CapStateKV         Capability = "state-kv"
	CapPorts           Capability = "ports"
	CapHooks           Capability = "hooks"
	CapRemoteDiscovery Capability = "remote-discovery"
)

// AllCapabilities returns the full closed Capability vocabulary in canonical
// order. This is the vocabulary, NOT the set advertised at runtime: Capabilities()
// returns only the subset backed by a wired command in the current build.
func AllCapabilities() []Capability {
	return []Capability{
		CapHelpJSON, CapResolveBlock, CapDryRun, CapPartialSuccess,
		CapLand, CapLandAtomic, CapStateKV, CapPorts, CapHooks, CapRemoteDiscovery,
	}
}

// Capabilities returns the capabilities advertised by the CURRENT build — only
// those backed by a wired command. v0 (M0–M3) lit the first four; M4 adds `land`
// now that `wi land` is wired, and `state-kv` now that `wi state cas` backs the
// namespaced compare-and-swap primitive (land-atomic stays dark until `--atomic`
// validate-all lands; ports/hooks at M5). This keeps the "capability ⇒ backing
// command" invariant true so an agent never branches on a capability that does nothing.
func Capabilities() []Capability {
	return []Capability{
		CapHelpJSON, CapResolveBlock, CapDryRun, CapPartialSuccess, CapLand, CapStateKV,
	}
}
