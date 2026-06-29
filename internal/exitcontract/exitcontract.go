// Package exitcontract is the single chokepoint between a command's typed outcome
// and the process exit code (DESIGN §3.2, IMPLEMENTATION_PLAN Wave A). It owns two
// things and nothing else:
//
//   - ExitCodeFor: the compiled error.kind -> exit-code table — the SHAPE-FAIL-MATRIX
//     subject. It is the ONE authority for the kind↔exit pairing the wire contract
//     pins, so the CLI Runner never hand-codes an exit number.
//   - Exit: the ONE wrapper around os.Exit. cmd/wi is its sole caller, so there is
//     exactly one os.Exit literal in the tree and an architecture test can forbid
//     bare os.Exit anywhere else.
//
// It sits just above internal/contract in the dependency stack (it imports the closed
// enums) and below internal/cli and cmd/wi. It assembles no envelopes and makes no
// branching decisions of its own — it is a pure lookup plus a terminal wrapper.
package exitcontract

import (
	"os"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// exitByKind is the compiled DESIGN §3.2 failure matrix: every closed error.kind to
// its process exit code. Exit codes are deliberately COARSER than kinds — several
// kinds share one code (dirty_worktree/conflict/already_exists all refuse at exit 4;
// lock_held/mirror_stale both exit 6) — so the precise distinction lives in the
// envelope's `kind` field and the exit code is the bucket an agent's shell branches
// on. remote_error has no dedicated code in the closed set, so it falls into the
// catch-all internal-error bucket (70) alongside `internal` (decision #X); the kind
// field still preserves the "remote vs internal" distinction for callers.
var exitByKind = map[contract.ErrorKind]contract.ExitCode{
	contract.KindUsage:         contract.ExitUsage,       // 64
	contract.KindNotFound:      contract.ExitNotFound,    // 3
	contract.KindDirtyWorktree: contract.ExitRefused,     // 4 — refused at exec
	contract.KindConflict:      contract.ExitRefused,     // 4 — refused at exec
	contract.KindLockHeld:      contract.ExitLocked,      // 6
	contract.KindMirrorStale:   contract.ExitLocked,      // 6 — only on the land path
	contract.KindNeedsApproval: contract.ExitNeedsApprov, // 5 — reachable once hooks ship (v2)
	contract.KindAlreadyExists: contract.ExitRefused,     // 4 — refused at exec
	contract.KindPartial:       contract.ExitPartial,     // 2 — durable, resumable
	contract.KindRemoteError:   contract.ExitInternal,    // 70 — no dedicated code (decision #X)
	contract.KindInternal:      contract.ExitInternal,    // 70
}

// ExitCodeFor returns the process exit code for an error kind per the §3.2 failure
// matrix. An unmapped kind (a future contract kind not yet wired, or a raw string
// that bypassed the typed enum) fails SAFE to ExitInternal — never a panic and never
// a spurious success. The totality of this table over contract.AllErrorKinds is
// enforced by SHAPE-FAIL-MATRIX, so in a correct build the default is unreachable for
// any real kind.
func ExitCodeFor(kind contract.ErrorKind) contract.ExitCode {
	if code, ok := exitByKind[kind]; ok {
		return code
	}
	return contract.ExitInternal
}

// MappedKinds returns the error kinds the table maps explicitly (unordered). It exists
// so SHAPE-FAIL-MATRIX can assert the table covers EXACTLY contract.AllErrorKinds —
// catching a dropped kind even when its code would collide with the defensive default.
func MappedKinds() []contract.ErrorKind {
	kinds := make([]contract.ErrorKind, 0, len(exitByKind))
	for k := range exitByKind {
		kinds = append(kinds, k)
	}
	return kinds
}

// Exit is the ONE wrapper around os.Exit (DESIGN §4: "the single os.Exit"). cmd/wi
// calls it with the code the Runner computed; no other package calls os.Exit, so the
// process has exactly one termination point and the no-bare-os.Exit architecture
// guard has a single sanctioned site to allowlist.
func Exit(code contract.ExitCode) {
	os.Exit(int(code))
}
