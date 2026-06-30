package contract

import "encoding/json"

// Envelope is the single wire type every wi command emits: exactly one per
// invocation, JSON by default. Field DECLARATION ORDER is the locked wire order
// (encoding/json marshals fields in declaration order); golden tests freeze it.
//
// Two hard shape invariants are enforced by MarshalJSON, not left to callers:
//   - "error" is ALWAYS present (null on success) — never omitted.
//   - "repos" is ALWAYS an array — never null — even when empty.
//
// capabilities/warnings/next are likewise coerced to [] so an agent can index
// them blind. See DESIGN.md §3.1.
type Envelope struct {
	SchemaVersion string       `json:"schema_version"`
	Capabilities  []Capability `json:"capabilities"`
	OpID          string       `json:"op_id"`
	Command       string       `json:"command"`
	OK            bool         `json:"ok"`
	Action        Action       `json:"action"`
	DryRun        bool         `json:"dry_run"`
	Repos         []RepoResult `json:"repos"`
	Warnings      []Warning    `json:"warnings"`
	Next          []string     `json:"next"`

	// Additive blocks — reserved, omitempty so v1/v2 stay minor bumps (DESIGN §3.1).
	// MVP-relevant blocks are defined now; land_state/tethers/ports/hooks are
	// folded at their milestone (M4/M5) and are additive to nil-in-v0 output.
	Resolve *ResolveBlock `json:"resolve,omitempty"`
	Planned []PlanItem    `json:"planned,omitempty"`
	Blocked []BlockItem   `json:"blocked,omitempty"`
	Help    *HelpBlock    `json:"help,omitempty"`
	Locks   []LockInfo    `json:"locks,omitempty"`

	// Error is null on success and MUST NOT carry omitempty — the null is contractual.
	Error *Error `json:"error"`
}

// MarshalJSON enforces the always-present-error and always-array invariants.
// The local type strips Envelope's methods to avoid infinite recursion while
// preserving field order and tags.
func (e Envelope) MarshalJSON() ([]byte, error) {
	type wire Envelope
	w := wire(e)
	if w.Capabilities == nil {
		w.Capabilities = []Capability{}
	}
	if w.Repos == nil {
		w.Repos = []RepoResult{}
	}
	if w.Warnings == nil {
		w.Warnings = []Warning{}
	}
	if w.Next == nil {
		w.Next = []string{}
	}
	return json.Marshal(w)
}

// RepoResult is the per-repo outcome inside an envelope. repos[] is always an array.
type RepoResult struct {
	Repo      string           `json:"repo"`
	Action    Action           `json:"action"`
	Branch    string           `json:"branch,omitempty"`
	Worktree  string           `json:"worktree,omitempty"`
	Mirror    string           `json:"mirror,omitempty"`
	SHA       string           `json:"sha,omitempty"`
	Stage     string           `json:"stage,omitempty"`
	MainState string           `json:"main_state,omitempty"`
	Freshness *MirrorFreshness `json:"mirror_freshness,omitempty"`
	Error     *Error           `json:"error,omitempty"`
}

// MirrorFreshness reports cached SSOT freshness. Read paths never dial the
// network; behind_origin is as-of the last fetch (DESIGN §5).
type MirrorFreshness struct {
	Stale                 bool   `json:"stale"`
	FetchedAt             string `json:"fetched_at,omitempty"`
	BehindOriginAsOfFetch int    `json:"behind_origin_as_of_fetch,omitempty"`
}

// Warning is a non-fatal note. Code is drawn from the closed WarningCode
// vocabulary (enums.go); message is free text agents never branch on.
type Warning struct {
	Code    WarningCode `json:"code"`
	Message string      `json:"message"`
	Repo    string      `json:"repo,omitempty"`
}

// Error is the failure payload. Agents branch on Kind (and the exit code),
// NEVER on Message text. Code is a stable sub-code; Help/DidYouMean are injected
// solely by the cli envelope writer.
type Error struct {
	Kind       ErrorKind `json:"kind"`
	Code       string    `json:"code,omitempty"`
	Message    string    `json:"message"`
	Repo       string    `json:"repo,omitempty"`
	Help       string    `json:"help,omitempty"`
	DidYouMean []string  `json:"did_you_mean,omitempty"`
}

// ResolveBlock is the path bundle returned by `wi resolve` (DESIGN §3.1).
type ResolveBlock struct {
	IsolateRoot string        `json:"isolate_root"`
	StateDir    string        `json:"state_dir"`
	Log         string        `json:"log"`
	Repos       []ResolveRepo `json:"repos"`
}

// ResolveRepo locates one repo's materialized paths within an isolate.
type ResolveRepo struct {
	Repo     string `json:"repo"`
	Worktree string `json:"worktree"`
	Mirror   string `json:"mirror"`
	Branch   string `json:"branch"`
}

// HelpBlock is the self-description payload for `wi help [topic]` — the wire form of the
// help-json capability (decision #HB). It is an additive, omitempty block: present only on
// help envelopes. Synopsis is always set; Topic/Usage are empty for the top-level overview
// (where Commands lists the whole surface) and set for a single command (where Commands is
// nil). The descriptive content lives here; the runnable follow-ups ride the envelope's
// top-level next[]. M5's agent-usability capstone enriches each command with self-describing
// flags[]/exit codes/kinds — additive to this v0 shape.
type HelpBlock struct {
	Topic    string        `json:"topic,omitempty"`
	Synopsis string        `json:"synopsis"`
	Usage    string        `json:"usage,omitempty"`
	Commands []HelpCommand `json:"commands,omitempty"`
}

// HelpCommand is one row of the command surface in a HelpBlock overview: the canonical
// name, its one-line synopsis, and its usage string.
type HelpCommand struct {
	Name     string `json:"name"`
	Synopsis string `json:"synopsis"`
	Usage    string `json:"usage"`
}

// LockInfo is one row of the `wi lock ls` inventory: a lock present in the project's
// locks dir, its read-only break-safety assessment, and the identity of its holder when
// known (DESIGN §7.3 / §7.4). It is an additive, omitempty block folded in at M4 — nil on
// every command that is not a lock inventory (lock ls / doctor). The four booleans are the
// machine-branchable verdict and are ALWAYS present (no omitempty) so an agent can index
// them blind; Reason is a human diagnostic an agent never branches on. Safe is the
// conjunction the heal layer gates on: true ONLY when the holder is proven dead on a
// flock-trustworthy filesystem (the other three booleans expose why).
type LockInfo struct {
	Key           string      `json:"key"`
	Safe          bool        `json:"safe"`
	FSTrustworthy bool        `json:"fs_trustworthy"`
	HolderKnown   bool        `json:"holder_known"`
	ProvenDead    bool        `json:"proven_dead"`
	Reason        string      `json:"reason"`
	Holder        *LockHolder `json:"holder,omitempty"`
}

// LockHolder is the identity recorded in a lock body: the process that took the lock.
// It is present (non-nil) on a LockInfo exactly when holder_known is true — a body-less
// or unparseable lock has no holder identity and is conservatively never breakable.
type LockHolder struct {
	PID    int    `json:"pid"`
	Host   string `json:"host"`
	BootID string `json:"boot_id"`
	OpID   string `json:"op_id"`
}

// PlanItem is a single planned action surfaced under --dry-run.
type PlanItem struct {
	Repo   string `json:"repo,omitempty"`
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
}

// BlockItem is a would-block verdict surfaced under --dry-run (dry-run always
// exits 0; the verdict lives here, not in a top-level error).
type BlockItem struct {
	Repo   string    `json:"repo,omitempty"`
	Kind   ErrorKind `json:"kind"`
	Reason string    `json:"reason"`
}
