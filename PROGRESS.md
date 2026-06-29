# wi — BUILD PROGRESS

Working log for the autonomous build loop. Source of truth for *intent*; the real
build state is always `go build ./... && go test ./...` (trust the build over this file).

Branch: `build/wi` (never commit to `main`). Spec: `DESIGN.md`. Order: `IMPLEMENTATION_PLAN.md`.

---

## Current position

- **Milestone:** **M2 COMPLETE; M3 IN PROGRESS** — domain command core fully landed and green
  (`internal/config` manifest read+validate, `internal/state` per-isolate runtime registry + durable
  partial success, `internal/isolate.New` N-repo orchestration under the `isolate-state:<task>` lock /
  stop-on-first-fail with durable not-rolled-back completed repos DESIGN §6.3, `internal/resolve.Bundle`
  the pure zero-I/O path-bundle projector behind `wi resolve`; the two `internal/git` isolate primitives
  `AddWorktree` + `CreateOwnedRef`/`OwnedRefSHA` underpin isolate). **M3 (the CLI surface → MVP) has
  begun:** `internal/exitcontract` landed (the single exit chokepoint owning the compiled
  `error.kind → exit-code` table `ExitCodeFor`, guard `SHAPE-FAIL-MATRIX`, + the sole `os.Exit` wrapper
  `Exit`), and `internal/cli.Emit` landed — the serialization chokepoint that writes EXACTLY ONE
  schema-valid envelope as a single compact line + newline, through the same `json.Marshal` path the
  contract goldens are frozen against (guard `SHAPE-ONE-ENVELOPE`); and the `internal/cli` ASSEMBLER
  landed — `Meta{OpID,Command,DryRun}` + the `Success`/`Failure` envelope constructors (sole owners of
  the **ok ⟺ error==nil** coupling + the always-stamped `schema_version`/`capabilities`/op_id/command/
  dry_run spine) + `ExitFor` (process exit as a pure function of the top-level error; `Blocked` is
  exit-neutral; resolves decision **#D** — partial = top-level `error.kind=partial`→exit 2, and dry-run
  exit-0 is honored by the planner leaving `Error` nil rather than a blanket override). Guards
  `SHAPE-ASSEMBLE` + `SHAPE-DRYRUN-EXIT0`. And `cli.RenderText` landed — the `--format text` lossless,
  path-scoped projection of the same assembled struct `Emit` serializes (no re-read of git/state; guard
  `SHAPE-TEXT-PROJECTION` proves losslessness via an independent reflection walk over string leaves;
  decision **#T**). And the `internal/cli` Runner EXECUTE-CORE landed — `Result` (typed domain outcome) +
  `Command` (`Run(ctx) (*Result, error)`) + `CommandError` (typed kind+hints; non-`*CommandError`→
  `internal`) + `Format`/`Render` (json→`Emit` / text→`RenderText`) + `Execute` (run → `envelopeFor`
  (Success/Failure + threads every additive block; a partial carries `repos[]` onto the top-level
  `error.kind=partial`) → `Render` → `ExitFor`), the SOLE assembler+serializer+exit-deriver so every
  command emits one envelope and exits identically; guard `RUN-PIPELINE`. And the DISPATCH layer landed —
  `Registry` (a `map[string]func(args)→(Command,error)` of dep-bound factories) + `Dispatch(ctx, w, clk,
  reg, args)` which parses globals (`--dry-run`/`--format`, recognized anywhere), resolves the subcommand
  by longest match (2-token `"isolate new"` beats 1-token `"isolate"`), mints `op_id` (`opid.New` via the
  clock — on EVERY path, incl. errors), builds `Meta`, and hands off to `Execute`; a parse error /
  unknown command / factory arg-rejection all collapse to ONE `usage` envelope (kind=usage → exit 64).
  Guard `DISPATCH-ROUTES`; resolves decision **#F** (hand-rolled stdlib parser, NOT cobra — zero-dep
  posture #6/#C, fixed small command surface). **The ENTIRE generic CLI pipeline — argv → dispatch →
  outcome → one envelope (json|text) → mapped exit — is complete and green, and the FIRST per-command
  handler is now plugged into it:** `wi resolve` (pure read — `state.Load`→`not_found`-on-`ErrNoRecord`,
  else `resolve.Bundle`→`Result{Action:read}`) + the `Deps`/`BuildRegistry` seam (the dep-bound factory
  map `cmd/wi` hands to `Dispatch`); guard `CMD-RESOLVE`, establishing the handler→`Result`/`CommandError`
  contract every remaining command follows. **The second handler — `wi init` — has now landed too:** it
  scaffolds a workspace at the resolved root (Bootstrap the `.wi/` subtree, then write a starter
  `wi.config.jsonc` LAST as an `O_EXCL` commit point; re-init → `already_exists` leaving the manifest
  intact), resolving the config WRITE path (decision #C, minimal starter-emitter) and the root-discovery
  decision (#G: root = cwd, init takes no operand). Guard `CMD-INIT`. **The third handler — `wi isolate
  new` — has now landed too** (the marquee command): it resolves each requested repo against the manifest
  → `isolate.RepoSpec` (undeclared → not_found, missing manifest → not_found+`wi init`, malformed → usage
  = decision #H), reads the minted op_id via `OpIDFrom(ctx)` into the durable `IsolateRecord`, drives
  `isolate.New`, and maps `StatusComplete`→created / `StatusPartial`→the durable `(result,
  *CommandError{partial})` carrying per-repo `repos[]` (#D) / `*lock.HeldError`→lock_held; the first
  handler needing a `*git.Git` (added `Deps.Git`, wired in `BuildRegistry`). Guard `CMD-ISOLATE-NEW`.
  **The fourth handler — `wi sync [<repo>…]` — has now landed too, with its domain core:** the
  `internal/sync` package (`sync.Run` — per-repo, under `repo:<name>`,
  `EnsureClone`→`Fetch`→`FastForwardBaseRef`→`mirror.Store`, CONTINUE-on-fail = decision #S; guard
  `SYNC-RUN`; the first `internal/` package to drive `gitexec.RunNetwork` end-to-end), and the thin
  `cmd_sync` handler over it (no operands → all declared repos; named → a subset; undeclared → not_found;
  missing manifest → not_found+`wi init`; reads each manifest URL into the `RepoSpec`; projects per-repo
  `synced`/`noop` + freshness; `StatusPartial`→durable `(result, *CommandError{partial, Action:synced})`;
  per-repo `error.kind` deferred to the gitexec classifier = decision #K). Added a `Clock` field to `Deps`.
  Guard `CMD-SYNC`. What remains for MVP: the last two
  per-command handlers (`repo add`/`isolate rm`), each a `Command` returning a
  `*Result`/`*CommandError` over the green M0–M2 core (never an envelope, never an exit code) with a
  `BuildRegistry` factory binding its deps + validating its args; then `cmd/wi` main (build the real registry + `clock.System`, call `Dispatch`, the
  single `os.Exit` via `exitcontract.Exit`); then CI + `.goreleaser.yaml` + Homebrew tap. Deferred
  enrichments pulled in when a command needs them: a `--` end-of-flags terminator + `did_you_mean` in
  dispatch, `isolate.New` resume (skip repos already `StageCreated`), per-repo base persisted in `state`
  (populates `resolve`'s `branch`), state KV + `cas`.
  M0 + M1 complete: contract spine, layout, opid, clock, testenv, lockfs, lock, `gitexec` runner+belt,
  full `internal/git` (resolve / ff / EnsureClone / IsClean / Fetch / DivergedCounts), complete
  `internal/mirror`, and both DESIGN §2 architecture invariants (INV-NO-LLM + INV-NO-NETWORK).
- **Wave:** A complete (modulo `NORM-CORRECT`, deferred to Wave B); in Wave B domain code (M2).

## Done

- **M2/B · `internal/sync` — the sync domain core (materialize + advance + record freshness)**
  (`sync.go` + `sync_test.go`). The orchestration core behind `wi sync`, built as its own domain package
  for symmetry with `internal/isolate` (the other materializing command) and hermetic testability — the
  thin `cmd_sync` handler that projects it onto the envelope is the NEXT unit. `sync.Run(ctx, l, g, clk,
  opID, specs) (Result, error)` syncs each `RepoSpec{Name, URL, Base}` in request order, each UNDER its
  own `repo:<name>` lock (the identical key v1 `land` takes — this is what linearizes the freshness race,
  DESIGN §6.1). Per repo (`syncOne`): `git.EnsureClone` (lazy — clone the SSOT detached at base tip on
  first sync, no-op once present) → `git.Fetch` (the network dial) → `git.ResolveRef(origin/<base>)` →
  `git.FastForwardBaseRef(base, originSHA)` (the SOLE base-ref mutation — ff-only update-ref, no
  checkout/merge, SSOT stays detached & pristine; a rewound/force-pushed origin → `*git.NonFastForwardError`
  leaves the ref untouched) → `mirror.Store` a `Snapshot{behind:0}` (behind=0 because the base was just
  advanced to exactly the fetched origin tip under the lock — provably current). **CONTINUE-on-fail**
  (decision below): repos are independent SSOTs, so a per-repo failure (unreachable origin, non-ff, held
  lock) is recorded in that repo's `RepoOutcome.Err` and the remaining repos are STILL synced; overall
  `Status=StatusPartial` if any failed. This deliberately DIFFERS from `isolate.New`'s stop-on-first-fail
  (an isolate is one coherent workspace whose completed set must stay a resumable prefix; sync has no such
  inter-repo dependency). `Run`'s Go-error return is reserved for an op-level failure — in v0 every failure
  is per-repo, so it returns nil error and reports via Status/Repos. The first `internal/` package to drive
  `gitexec.RunNetwork` end-to-end (clone+fetch). Guard `SYNC-RUN` (`sync_test.go`, hermetic `testenv` +
  real git against local `file://` origins): fresh repo → lazy clone + base advanced to origin tip +
  behind-0 freshness persisted (`mirror.Load` round-trip) + SSOT working tree pristine (`IsClean`); origin
  advances (a pushed commit) → a second sync FAST-FORWARDS the on-disk base ref to the new tip;
  continue-on-fail → an unreachable repo listed FIRST fails yet the reachable repo after it still syncs,
  overall partial. Each SHA assertion checks the base ref ON DISK (`g.ResolveRef`), independent of the
  returned snapshot. Mutant demonstrated RED-then-reverted: drop the `g.FastForwardBaseRef` call (`if
  false {…}`) → after the origin advances the base ref stays frozen at the seed tip (`48f4258c…`, the
  testenv golden) instead of advancing → `TestSyncFastForwardsToNewOriginTip` RED on the on-disk assertion,
  while the fresh-materialize test (FF is a no-op when seed==origin) and continue-on-fail test stay GREEN —
  isolating the mutant to the advance path. Full `go build ./… && go vet ./… && go test ./…` GREEN
  (21 packages).
- **M3/B · `wi isolate new` handler — the marquee command** (`cmd_isolate_new.go` + `Deps.Git` +
  `BuildRegistry` line + `cmd_isolate_new_test.go`). The seam between the isolate domain core (durable
  partial success, DESIGN §6.3) and the envelope contract — and the FIRST handler needing a `*git.Git`
  (added `Git *git.Git` to `Deps`, wired in `BuildRegistry`; read-only commands leave it nil).
  `newIsolateNewCommand(l, g, args)` validates `<task>` (a safe segment via `layout.ValidateSegment` →
  usage) + ≥1 `<repo>`; repo names are NOT segment-checked at the factory (an undeclared one is
  not_found, not usage — the operand is well-formed, it just names nothing wi manages). `isolateNewCmd.Run`:
  (a) `config.Load(l.Config())` — missing manifest (`fs.ErrNotExist`) → `not_found` + `wi init` hint, a
  malformed manifest → `usage` (decision #H: user-fixable input, exit 64, NOT internal); (b) resolves
  each requested repo via `cfg.Lookup` → `isolate.RepoSpec{Name, Base(effective — defaults applied by
  config.Parse)}`, an undeclared repo → `not_found` naming it (resolved BEFORE any materialization, so a
  bad name writes no state record); (c) reads the minted op_id via `OpIDFrom(ctx)` (the CTX-OPID payoff)
  and drives `isolate.New(ctx, l, g, task, opID, specs)`; (d) maps Status onto the return convention —
  `StatusComplete` → `Result{Action:created, Repos:[…projected]}`; `StatusPartial` → the DURABLE PARTIAL
  `(result, *CommandError{Kind:partial, Action:created})` carrying per-repo `repos[]` (decision #D, the
  only both-non-nil case); a returned `*lock.HeldError` → `*CommandError{Kind:lock_held}` (exit 6); any
  other returned err → internal. `projectRepoOutcome` maps `isolate.RepoOutcome`→`contract.RepoResult`
  (Stage==created → action created else noop; worktree path + tip sha; a per-repo `Error{kind:internal}`
  on exactly the failed repo — Mirror/Branch empty for v0, a refined per-repo Error.Kind awaits the
  gitexec stderr classifier). It never assembles an envelope or picks an exit code. Guard `CMD-ISOLATE-NEW`
  (`cmd_isolate_new_test.go`, hermetic `testenv` + real `git`, driven THROUGH `BuildRegistry`'s factory):
  complete path (2 repos created + the durable `IsolateRecord.OpID` == the op_id injected into the ctx —
  proving the seam pays off); durable partial (web has no SSOT → both a `*CommandError{partial,
  Action:created}` AND a result whose repos[] shows api created / web errored, durable registry api
  created / web pending); undeclared-repo → not_found naming the repo + NO record written; missing
  manifest → not_found + `wi init` help + NO record; factory arg validation (<2 args or a traversing task
  → usage, safe task+repo → a Command). Mutant demonstrated RED-then-reverted: on `StatusPartial` return
  `(result, nil)` instead of the partial `*CommandError` → a partial is mis-reported as a clean success →
  `TestIsolateNewDurablePartial` RED (`want *cli.CommandError, got <nil>`). Full `go build ./… &&
  go vet ./… && go test ./…` GREEN (20 packages).
- **M3/B · op_id propagation via context — the `WithOpID`/`OpIDFrom` seam** (`opctx.go` +
  `Execute` injection + `opctx_test.go`). The small prerequisite that unblocks every handler which must
  record the op identity in DURABLE state (the first being `isolate new` → `IsolateRecord.OpID`). The
  `Command` interface stays minimal (`Run(ctx)` only) — the per-invocation op_id is runtime context, so
  it rides the `context.Context`: `WithOpID(ctx, opID)` stores it under an unexported zero-size key
  type (no cross-package collision), `OpIDFrom(ctx)` reads it back (or `""` outside the pipeline).
  `Execute` injects `m.OpID` into the ctx ONCE, before `cmd.Run`, so a handler reads the SAME id the
  envelope reports rather than minting a divergent one (which would split a single invocation's
  correlation id across the envelope and the state record). Guard `CTX-OPID` (`opctx_test.go`): the
  load-bearing test drives a `capturingCommand` THROUGH `Execute` and asserts it observed `Meta.OpID`
  (proves the wiring, not just the helper); a round-trip + bare-ctx→`""` test covers the helper. Mutant
  demonstrated RED-then-reverted: drop the `ctx = WithOpID(ctx, m.OpID)` injection in `Execute`
  (`if false {…}`) → the command observes `""` → `TestExecuteInjectsOpIDIntoContext` RED. Full
  `go build ./… && go vet ./… && go test ./…` GREEN (20 packages).
- **M3/B · `wi init` handler — workspace scaffold** (`cmd_init.go` + `BuildRegistry` line +
  `cmd_init_test.go`). The second per-command handler and the first WRITE command (resolves decision #C's
  deferred write path with a minimal starter-manifest emitter; the AST-preserving `repo add` edit path
  stays deferred to its own unit). `newInitCommand(l, args)` takes NO positional operand — init DEFINES
  the workspace at the resolved root (decision **#G**: root = cwd, the layout `cmd/wi` resolves at
  startup; a surplus arg → `*CommandError{Kind:usage}`; an explicit `--root`/`-C` override + parent
  walk-up are deferred, both additive/contract-neutral). `initCmd.Run` Bootstraps the `.wi/` runtime
  subtree (idempotent) THEN writes the `starterManifest` (a commented empty-but-valid JSONC skeleton)
  LAST as the commit point — an `O_EXCL` create, so the manifest's presence reliably marks a completed
  init and a re-init refuses cleanly with `already_exists` (→ exit 4) rather than clobbering a real
  manifest; Bootstrap precedes the write so a Bootstrap failure leaves no manifest and a retry starts
  clean. No git, no network. Guard `CMD-INIT` (`cmd_init_test.go`, real `layout.Resolve`'d but
  NOT-Bootstrapped temp root — the analog of an uninitialized cwd, driven THROUGH `BuildRegistry`'s
  factory): a fresh dir → a `created` Result whose written manifest ROUND-TRIPS through the real
  `config.Load` (init's emitter dogfoods the config reader so they cannot drift) + the `.wi/state`
  subtree exists (Bootstrap ran), with paths INDEPENDENTLY derived (scheme literals joined over the root,
  not via `l.Config()`/`l.StateDir()`); a re-init → `*CommandError{Kind:already_exists}` with the
  existing manifest preserved byte-for-byte; factory arg validation (any operand → usage, none → a
  runnable Command). Mutant demonstrated RED-then-reverted: open the manifest with `O_TRUNC` instead of
  `O_EXCL` (clobber-on-reinit) → a re-init silently rewrites the manifest and returns `created` (no
  `fs.ErrExist`→`already_exists`) → `TestInitOnExistingProjectIsAlreadyExists` RED (got a result/nil
  error, want `*cli.CommandError`). Reverted → full `go build ./… && go vet ./… && go test ./…` GREEN
  (20 packages).
- **M3/B · first per-command handler — `wi resolve` + the `Deps`/`BuildRegistry` seam** (`cmd_resolve.go`
  + `cmd_registry.go` + `cmd_resolve_test.go`). The first real `Command` plugged into the green generic
  pipeline, plus the registry-builder seam `cmd/wi` will use. `Deps{Layout}` carries the
  already-resolved startup dependencies the factories close over (grows additively — a new dep is a new
  field, existing handlers untouched); `BuildRegistry(Deps) Registry` wires each subcommand's canonical
  name → its arg-parsing factory (adding a command = one line here + its `cmd_<name>.go`, no change to
  `Dispatch`). `newResolveCommand(l, args)` is the `resolve` factory: validates the positional args
  (exactly one task, a safe segment via `layout.ValidateSegment`) → a `*CommandError{Kind:usage}` on
  failure (the traversal check lives HERE so a bad task name is a clean usage refusal, not an opaque
  internal error from state/layout), else binds task+layout into `resolveCmd`. `resolveCmd.Run` is a
  PURE projection (DESIGN §3.1, §map 166): `state.Load(StateDir, task)` → on `state.ErrNoRecord` a
  `not_found` refusal (operator hint points at `wi isolate new`), any other load error unclassified →
  internal, else `resolve.Bundle` → `Result{Action: read, Resolve: &block}`. No git, no network, no
  mutation. Handlers live in `package cli` (top of the dep stack — importing `layout`/`state`/`resolve`
  is one-directional, no cycle). Guard `CMD-RESOLVE` (`cmd_resolve_test.go`, real bootstrapped layout +
  stored record, driving the command THROUGH `BuildRegistry`'s factory — the exact path `cmd/wi` uses):
  a present 2-repo record → a `read` Result whose resolve block carries INDEPENDENTLY-derived paths
  (scheme literals joined over the normalized root, not via the layout accessors the impl uses); a
  missing record → `*CommandError{Kind:not_found}` naming the task; factory arg validation (0/2 args +
  a traversing name → usage, one safe arg → a runnable Command). Mutant demonstrated RED-then-reverted:
  `Run` skips the `errors.Is(err, state.ErrNoRecord)` branch (`if false && …`) → a missing isolate
  surfaces as a plain `*errors.errorString` (→ internal) not a `*CommandError{not_found}` →
  `TestResolveMissingIsolateIsNotFound` RED. Full `go build ./… && go vet ./… && go test ./…` GREEN (19
  packages). Establishes the handler→`Result`/`CommandError` contract every remaining command follows.
- **M3/B · `internal/cli` DISPATCH layer — `Registry` + `Dispatch` (argv → Command + Meta → Execute)**
  (`dispatch.go` + `dispatch_test.go`). The front half of the Runner, sitting on top of the green
  `Execute` core (DESIGN §3, §4). `Registry` = `map[string]func(args []string) (Command, error)`: a
  factory per canonical command string (`"init"`, `"isolate new"`), each closing over the command's
  deps (layout/clock/git/…) so `Dispatch` stays dependency-agnostic and a factory carries only the
  parsed args; a factory rejecting its args → a `usage` envelope. `Dispatch(ctx, w, clk, reg, args)
  (contract.ExitCode, error)`: mint `op_id` FIRST (`opid.New(clk.Now(), clk.Rand())` — every path,
  incl. errors, carries a correlation id; a crypto/rand failure is surfaced as `kind=internal`), parse
  globals, build `Meta{OpID, Command, DryRun}`, resolve the subcommand by LONGEST match (2-token
  `"isolate new"` beats 1-token `"isolate"`), then `reg[name](rest)` → `Execute`. A parse error, an
  unknown command, or a factory arg-rejection ALL become one `usage` envelope (`kind=usage` → exit 64)
  via `emit` (= the same `Render`+`ExitFor` wiring `Execute` uses), so every dispatch path — including
  the ones that never reach a `Command` — still emits EXACTLY ONE envelope. `parseGlobals` is a forgiving
  single pass: `--dry-run`, `--format <v>`, `--format=<v>` recognized ANYWHERE (v0 command args are
  plain names/URLs that never start with `--`), everything else positional; an absent/invalid `--format`
  value is a usage error; format defaults to `json`. A returned Go error is reserved for an
  infrastructure write failure (propagated from `emit`/`Execute`). Resolves decision **#F** (hand-rolled
  stdlib parser, NOT cobra). Guard `DISPATCH-ROUTES` (`dispatch_test.go`, fake `Registry` of recording
  factories + the `fakeCommand`/`decodeOne` helpers reused from `run_test.go`): a known 1-token command
  routes + threads its `Result` (exit 0, `command` stamped); a 2-token command resolves AND forwards the
  trailing args; an unknown command → `kind=usage` exit 64 naming the command, with NO factory run; a
  factory arg-rejection → `kind=usage` exit 64; `--dry-run` threads to `env.DryRun` without leaking into
  command args; `--format text` reaches `RenderText` (non-JSON, shows `init`+`OK`); `--format yaml` is a
  usage error; `op_id` is `opid.Valid` on BOTH the success and the unknown-command paths. Mutant
  demonstrated RED-then-reverted: `resolveCommand` ignores the parsed name and always returns a fixed
  real command (`return "init", positional, true`) → `TestDispatchRoutesUnknownToUsage` RED (an unknown
  name wrongly runs a real command, exit 0 not 64) AND `TestDispatchRoutesTwoTokenCommand` RED (command
  stamped `"init"`, args not forwarded). Reverted → full `go build ./… && go vet ./… && go test ./…`
  GREEN (19 packages). **Deferred enrichments** (pull in when a command needs them): a `--` end-of-flags
  terminator + `did_you_mean` suggestions on the unknown-command path.
- **M3/B · `internal/cli` Runner execute-core — `Result`/`Command`/`CommandError` + `Format`/`Render` +
  `Execute`** (`run.go` + `run_test.go`). The single wiring point that turns a command's typed outcome
  into exactly one envelope + a process exit code (DESIGN §3, §4). `Result` is the typed domain outcome a
  handler returns (Action + Repos + the additive Resolve/Planned/Blocked + Warnings/Next) — a handler
  returns plain data and NEVER assembles the wire shape or picks a code. `Command` is `Run(ctx) (*Result,
  error)`, constructed by the (next) dispatcher with its args/deps already bound; its return convention is
  documented: `(result,nil)`=success, `(nil,*CommandError)`=classified failure, `(nil,plainError)`=
  internal, `(result,*CommandError{Kind:partial})`=durable partial. `CommandError` is the typed error
  that selects a failure envelope's `error.kind` + operator hints (repo/help/did_you_mean); a non-
  `*CommandError` maps to `kind=internal` (an unclassified failure is a bug, surfaced not hidden). `Format`
  (`json`|`text`) is a CLI presentation concern owned by `internal/cli` (NOT a wire enum — contract keeps
  those), and `Render(w, env, format)` dispatches json→`Emit` / text→`RenderText` over the SAME assembled
  struct. `Execute(ctx, w, m, format, cmd)` runs the command, maps `(*Result|error)`→Envelope via the
  unexported `envelopeFor` (Success/Failure + threads every additive block; a partial threads the result's
  per-repo detail ONTO the failure so "what completed" survives next to the top-level `error.kind=partial`,
  decision #D), serializes via `Render`, and returns `ExitFor(env)` — so every command, whatever the
  outcome, emits one envelope and exits the same way. A returned Go error is reserved for an INFRASTRUCTURE
  write failure (→ `ExitInternal`); a domain failure rides inside the emitted envelope. Guard `RUN-PIPELINE`
  (`run_test.go`, fake `Command`): drives all six outcome classes end-to-end through the real pipeline —
  success (blocks threaded, exit 0), `CommandError` (kind+hints preserved, matrix exit), plain error
  (kind=internal, exit 70), durable partial (ok:false + `kind=partial` BUT `repos[]` carried, exit 2),
  `--format text` dispatch (not JSON, shows `FAILED`+kind), dry-run blocked-verdict (exit-neutral, exit 0).
  Mutant demonstrated RED-then-reverted: drop the partial result-merge (`if false && r != nil`) → the
  partial loses its per-repo detail → `TestExecutePartialCarriesReposAndExitsTwo` RED. Full `go build ./… &&
  go vet ./… && go test ./…` GREEN (19 packages).
- **M3/B · `internal/cli.RenderText` — the `--format text` lossless projection** (`text.go` +
  `text_test.go`). `RenderText(io.Writer, contract.Envelope) error` is a PURE, path-scoped projection of
  the SAME assembled struct the JSON path (`Emit`) serializes — "no extra facts, no dropped facts"
  (DESIGN §3.1) — taking the already-built envelope and only reformatting it (no new I/O, never a
  git/state re-read), so the two wire forms can never disagree. Layout: a status header (command + OK/
  FAILED + a `[dry-run]` marker), the op_id/action/schema metadata line + capabilities, then one section
  per populated block (repos with their path bundle + per-repo freshness/error, resolve, planned,
  blocked, warnings, top-level error, next); empty optionals are omitted (absence drops no fact).
  Resolves decision **#T** (text renders every field, losslessly; formatting is a sectioned report).
  Guard `SHAPE-TEXT-PROJECTION` (`text_test.go`): losslessness is verified INDEPENDENTLY — a reflection
  walk (`collectStringLeaves`) enumerates every non-empty string leaf of a maximal envelope (free fields
  carry unique `z…z` sentinels, enums carry real mutually-distinct values) and asserts each appears in
  the render; inline non-vacuity (≥25 leaves found + a never-present sentinel that must NOT match) keeps
  the containment loop honest; a second test covers the non-string facts the walk can't (ok/dry-run
  bools, the freshness `behind` int) and that success/error/partial renders are distinguishable. Mutant
  demonstrated RED-then-reverted: drop the worktree line from `renderRepo` (`if false && …`) → the
  `"zworktreeonez"` leaf is absent → `TestRenderTextIsLossless` RED (naming the exact dropped fact).
  Full `go build ./… && go vet ./… && go test ./…` GREEN (19 packages).
- **M3/B · `internal/cli` assembler — `Meta` + `Success`/`Failure` + `ExitFor`** (`assemble.go` +
  `assemble_test.go`). The outcome→envelope→exit core that feeds `Emit`. `Meta{OpID, Command, DryRun}`
  is the per-invocation context the Runner threads in; the unexported `spine(Meta)` stamps the
  always-identical fields ONCE (`schema_version`=`contract.SchemaVersion`, `capabilities`=
  `contract.Capabilities()`, op_id/command/dry_run) so handlers return plain structs and never touch the
  wire. `Success(m, action, repos)` and `Failure(m, action, errPayload)` are the SOLE envelope
  constructors — they enforce the **ok ⟺ error==nil** coupling at the only two call sites that can build
  an envelope (Success → ok=true, nil error; Failure → ok=false, a stored *copy* of the error so the
  caller can't alias-mutate it); additive blocks (`Resolve`/`Planned`/`Blocked`) + `Warnings`/`Next` are
  command-specific and set by the caller on the returned value (they don't bear on the coupling).
  `ExitFor(env)` derives the process code as a **pure function of the top-level error** — nil → exit 0;
  else `exitcontract.ExitCodeFor(env.Error.Kind)` (the single §3.2 authority, so `partial`→2 falls out
  for free). It deliberately does NOT consult `Blocked` or `DryRun`. Resolves decision **#D** (how
  "every --dry-run → exit 0" is honored AND how partial success is represented). Guards `SHAPE-ASSEMBLE`
  (Success/Failure couplings + always-set common fields) + `SHAPE-DRYRUN-EXIT0` (`ExitFor` couples
  error→code via the matrix incl. partial→2, a populated `Blocked` is exit-neutral, and a genuine
  top-level error on a dry-run is NOT swallowed to 0). Both mutants demonstrated RED-then-reverted:
  `Success` sets `ok=false` → the coupling assertion RED; `ExitFor` returns `ExitRefused` when
  `len(Blocked)>0` → the dry-run-with-blocked exit-neutrality assertion RED. Full `go build ./… &&
  go vet ./… && go test ./…` GREEN (19 packages).
- **M3/B · `internal/cli.Emit` — the one-envelope serialization chokepoint** (`emit.go` +
  `emit_test.go`). `Emit(io.Writer, contract.Envelope) error` writes EXACTLY ONE envelope: a single
  compact JSON line + one trailing newline, and nothing else (DESIGN §3.1). It serializes through
  `contract.Envelope`'s own `json.Marshal` path — the SAME bytes the contract goldens and the published
  schema are frozen against — so emitted output can never drift from the contractual wire shape (no
  alternate Encoder, no HTML-escaping divergence); `MarshalJSON` already guarantees the always-present
  `error` + always-array `repos`/`capabilities`/`warnings`/`next`. First file of the `internal/cli`
  uniform-pipeline package. Resolves decision **#E** (emit output convention). Guard `SHAPE-ONE-ENVELOPE`
  (`emit_test.go`): decode the stream → exactly one top-level value then `io.EOF`; the four list fields
  decode as arrays + `error` key present; emitted bytes validate against the embedded schema (success +
  error); single-trailing-newline + single-compact-line; and the payload byte-equals `json.Marshal(env)`.
  Both mutants demonstrated RED-then-reverted: emit-twice → the EOF assertion RED; drop-the-newline → the
  newline assertion RED. Full `go build/vet/test ./…` GREEN (18 packages).
- **M3/A+B · `internal/exitcontract` — the exit chokepoint + failure matrix** (`exitcontract.go` +
  `exitcontract_test.go`). The single authority between a command's typed outcome and the process exit
  code (DESIGN §3.2, PLAN Wave A). `ExitCodeFor(contract.ErrorKind) contract.ExitCode` is the compiled
  §3.2 failure matrix (usage→64, not_found→3, dirty_worktree/conflict/already_exists→4, needs_approval→5,
  lock_held/mirror_stale→6, partial→2, internal→70), failing SAFE to `ExitInternal` for an unmapped kind;
  `MappedKinds()` exposes coverage for the totality check; `Exit(code)` is the SOLE `os.Exit` wrapper so
  there is exactly one termination point (the no-bare-`os.Exit` architecture guard, a later unit, will
  allowlist only this site). Resolves decision **#X** (`remote_error` → 70). Guard `SHAPE-FAIL-MATRIX`
  (`exitcontract_test.go`): an INDEPENDENT literal copy of the §3.2 table asserts every pairing; a
  separate totality test asserts the table covers EXACTLY `contract.AllErrorKinds()` and every produced
  code is in `contract.AllExitCodes()`; plus an unknown-kind-fails-to-internal test. Both mutants
  demonstrated RED-then-reverted: (1) `KindLockHeld`→`ExitRefused` reddened only the matrix value test;
  (2) dropping `KindInternal` (code collides with the default) left the value test GREEN but reddened the
  totality test — proving the two checks are non-redundant. Full `go build ./… && go vet ./… && go test
  ./…` GREEN (17 packages).
- **M2/B · `internal/resolve` — pure path-bundle projector** (`resolve.go` + `resolve_test.go`).
  `Bundle(layout.Layout, state.IsolateRecord) (contract.ResolveBlock, error)` is a PURE, zero-I/O
  projection (no config, no git, no network, no filesystem read) answering "given a task, where is
  everything?": `isolate_root`=TaskDir, `state_dir`=StateDir, `log`=LogDir, and per repo
  `worktree`=Isolate (`isolas/<task>/<repo>`), `mirror`=Repo (`repos/<repo>` SSOT clone), `branch`=`""`
  (v0 worktrees are DETACHED, DESIGN §5). Every path sourced from `internal/layout`, never hand-assembled;
  the CLI owns `state.Load` + `ErrNoRecord`→`not_found`. Resolves decision **#R**. Guard `RESOLVE-BUNDLE`:
  a 2-repo record projects to exact hand-written golden paths (independent join), + empty-record→non-nil
  empty slice + traversing-name→validation error. Mutants demonstrated: `mirror := worktree`→both Mirror
  assertions RED; drop a repo→count RED. **M2 turns COMPLETE.**
- **M0/A · contract enums** — `internal/contract/enums.go`: closed sets `Action` (6),
  `ErrorKind` (11), `ExitCode` (9), `Capability` (10 vocab / 4 advertised) + `SchemaVersion`.
  Guard `SHAPE-ENUM-DOUBLE-ENTRY` (`enums_test.go`): independent literal copies; drift /
  duplicate / subset checks; inline non-vacuity proof. Real-source mutant (added `"timeout"`
  to `AllErrorKinds`) confirmed RED, then reverted → GREEN.
- **M0/A · Envelope wire type** — `internal/contract/envelope.go`: the `Envelope` struct with
  locked field declaration order + custom `MarshalJSON` enforcing `error:null` (never omitted)
  and `repos`/`capabilities`/`warnings`/`next` always-`[]` (never null). Nested wire types
  `RepoResult`, `MirrorFreshness`, `Warning`, `Error`, `ResolveBlock`, `ResolveRepo`, `PlanItem`,
  `BlockItem` (additive blocks `resolve`/`planned`/`blocked` are omitempty per DESIGN §3.1).
  Guard `SHAPE-ENVELOPE-INVARIANTS` (`envelope_test.go`): golden success/error bytes,
  error-always-present, repos-always-array, frozen 11-key top-level order + a non-vacuous
  order-extractor proof. Mutant (added `,omitempty` to `Error`) confirmed RED on
  `TestEnvelopeGoldenSuccess`/`TestEnvelopeErrorAlwaysPresent`, then reverted → GREEN.
- **M0/A · WarningCode closed vocab** (open decision #1, RESOLVED) — `internal/contract/enums.go`:
  closed `WarningCode` set `{hydrate_skipped, base_behind_ssot}` + `AllWarningCodes()`; `Warning.Code`
  retyped `string`→`WarningCode`. Extended `SHAPE-ENUM-DOUBLE-ENTRY` (`wantWarningCodes` +
  `TestWarningCodeDoubleEntry` + uniqueness). Real-source mutant (`"stray_mutant"`) confirmed RED,
  reverted → GREEN. Staleness deliberately NOT a warning — it lives in `mirror_freshness.stale`.
- **M0/A · Envelope JSON Schema SSOT** — `schema/envelope.schema.json` (draft 2020-12,
  `additionalProperties:false`, all 11 top-level keys `required` incl. `error`, closed enums for
  action/capability/error.kind/warning.code, `schema_version` const `"1.0"`). Embedded via new
  `package schema` (`schema/schema.go`, `//go:embed`) for the future `wi schema` command + test use.
  First external dep: `santhosh-tekuri/jsonschema/v6` (test-only import; benign transitive
  `x/text`,`regexp2`). Guard `SHAPE-SCHEMA` (`internal/contract/schema_test.go`): both goldens
  validate; a 7-case malformed corpus (extra key, missing `error`, bad enums, wrong version, null
  repos) is rejected; + non-vacuity proof that the validator still accepts the known-good golden.
  Mutant (top-level `additionalProperties:true`) confirmed RED on `TestSchemaRejectsInvalid`,
  reverted → GREEN. **Decision:** reserved additive blocks (`land_state`/`ports`/`hooks`/`tethers`)
  are NOT pre-declared — added to schema+struct together at their milestone (minor bump) so the
  upcoming `SHAPE-FINGERPRINT` schema↔struct tripwire stays exact.
- **M0/A · SHAPE-FINGERPRINT + contract.lock.json** — `internal/contract/fingerprint_test.go` +
  `internal/contract/testdata/contract.lock.json`. One frozen tripwire over the whole contract:
  `SchemaVersion` + sha256(schema bytes) + a reflection-derived canonical `struct_shape` (json tags
  incl. `,omitempty`, recursing through pointers/slices/nested types) + its sha256. The lock file IS
  the fingerprint (env-gated regen via `WI_UPDATE_CONTRACT_LOCK=1`); **decision:** no duplicate Go
  `SchemaFingerprint` const, to avoid double-maintenance. `TestFingerprintIsNonVacuous` proves the
  shape extractor catches added field / retype / omitempty change. Real-source mutant (added a
  `Mutant string` field tagged `json:"-"`) confirmed it turns ONLY `TestContractFrozen` RED while the
  marshal/golden tests stay green — exactly the silent-drift class the fingerprint exists to catch.
  Reverted → GREEN. **Wave A contract spine is now structurally complete** (schema/struct/enums all
  guarded + locked); remaining Wave-A write-first items are INV-NO-LLM, META-VACUITY, NORM-CORRECT.

- **M0/A · INV-NO-LLM** — `internal/invariants` (new package owning DESIGN §2 architecture guards).
  `nollm_test.go` walks the module graph (`go.mod` + `go.sum`, the full transitive closure) and
  fails if any curated LLM/agent-SDK token appears in a module path; pure `scanForDenylisted` +
  cwd-walk-up `moduleRoot`. `TestNoLLMScannerIsNonVacuous` exercises the same detector on a synthetic
  `go-openai` line. Real-source mutant (appended a `// ...go-openai` comment to `go.mod`) confirmed
  `TestNoLLMDependencies` RED (flagged `[openai go-openai]`), reverted via `git checkout` → GREEN.
- **M0/A · WI_FAULT harness + META-VACUITY** — `internal/fault`: the deterministic fault-injection
  seam (`fault.Active(id)` reading the `WI_FAULT` env, exact per-entry match; inert when unset) that
  future HEAL/crash guards consult so their mutant is "set `WI_FAULT=<id>`" rather than a source edit
  — the harness IMPLEMENTATION_PLAN §2 lists as a Wave-A deliverable. `META-VACUITY`
  (`meta_vacuity_test.go`) re-execs the test binary to run a reference guard twice: under
  `WI_FAULT=meta.reference` it MUST fail (a fault can redden a guard), with no fault it MUST pass.
  Unit test `TestActiveIn` pins exact (non-substring) matching. Real-source mutant (made `refSubject`
  ignore the fault) confirmed `TestMetaVacuity` RED ("harness is vacuous"), reverted → GREEN.
- **Wave A is COMPLETE** (modulo `NORM-CORRECT`, intentionally deferred to Wave B): contract spine
  (enums/envelope/schema/fingerprint-lock), INV-NO-LLM, and the WI_FAULT/META-VACUITY methodology
  harness are all in and green. M0 now proceeds to its non-contract packages.
- **M0 · `internal/layout` (path core)** — `layout.go` + `layout_test.go`: `Layout` is the SOLE
  owner of every wi path (DESIGN §1, §4). `New(absRoot)` (cleans, requires absolute) + accessors
  `Root/Config/ReposDir/IsolasDir/WiDir`, the seven `.wi/` subtree dirs
  (`locks/state/log/mirrors/land/ports/trust` via one `WiSubdirs()` SSOT), and the input-bearing
  `Repo/TaskDir/Isolate`. `validSegment` is the chokepoint blocking traversal: rejects
  empty / `.` / `..` / path-separator (either flavor) / NUL / absolute, so user repo/task names
  can't escape the tree. Guards `LAYOUT-PATHS` (hand-written golden relative paths — independent
  copy of the scheme) + `LAYOUT-SAFE` (reject corpus + an accept floor). Mutants confirmed:
  `isolas`→`isolate` → `TestPaths` RED; `validSegment`→always-nil → reject cases RED; →always-error
  → accept floor RED. All reverted → GREEN. **Deferred to the post-`testenv` unit:** `Bootstrap`
  (mkdir the `.wi/` subtree) + EvalSymlinks root normalization — both need an existing on-disk root,
  so they wait for the real-FS `internal/testenv` harness (also M0).

- **M0 · `internal/cli/opid` (op-id format)** — `opid.go` + `opid_test.go`: mints/validates the one
  volatile envelope field (DESIGN §2/§3.1/§8). Root id `op_<base36ts>_<base32rand>` (ts = Unix
  millis base36; rand = 5 bytes → 8 chars lowercase unpadded base32), child suffix `.<n>` (n≥1,
  nests). `New(now, io.Reader)` is pure/deterministic (reads exactly `randLen`, errors on short read
  — never truncates); `Child`; `Valid` over a frozen regex. **Decisions recorded** (#A below): ms
  time unit, 5 random bytes, n≥1 no-leading-zero child index. Guard `OPID-FORMAT` pins the shape from
  independent angles (zero-bytes→`"aaaaaaaa"` pins the base32 encoding; inverse `ParseInt(base36)`
  pins base + ms unit; reject corpus + accept floor). Mutants confirmed: `UnixMilli`→`Unix`,
  `randLen` 5→4, prefix `op_`→`wi_` all → RED. Reverted → GREEN.

- **M0 · `internal/clock` (time/rand seam)** — `clock.go` + `clock_test.go`: the `Clock` interface
  (`Now() time.Time` + `Rand() io.Reader`) funnels wi's two volatile inputs (DESIGN §2 determinism,
  §4). `System` = real UTC time + `crypto/rand` (a local syscall, honors no-hidden-network §2.3);
  `Fake(instant, seed)` = fixed advanceable instant + a self-contained splitmix64 byte stream
  (`detReader`, ours not `math/rand` so the sequence is stable across stdlib changes; never
  short-reads). Guard `CLOCK-DETERMINISM` pins reproducible (same seed → same stream + same op_id via
  `opid.New`), seed-sensitive (diff seed → diff stream), non-degenerate (not all-zero), and System
  live (UTC + crypto/rand varies). Mutants confirmed: `Fake.Rand`→`crypto/rand` → reproducible RED;
  `NewFake` ignores seed → seed-sensitive RED. Reverted → GREEN. (Compile-time `var _ Clock` for both
  impls.)

- **M0 · `internal/testenv` (hermetic real-git harness)** — `testenv.go` + `testenv_test.go`: the
  sandbox every FS/git unit test runs inside (PLAN §M0). `New(t)` → an `Env` with an
  EvalSymlinks-normalized `t.TempDir` root + a fully isolated git environment; `Git(t,dir,args…)`
  runs git under it (fails on non-zero, returns trimmed stdout); `SeedOrigin(t,name)` makes a bare
  origin with one deterministic commit on `main` (local `clone --bare`, no network). Isolation:
  `GIT_CONFIG_GLOBAL`/`SYSTEM`=`/dev/null` + `GIT_CONFIG_NOSYSTEM=1`, fixed identity + fixed
  author/committer **dates** (reproducible SHAs), `LC_ALL=C` (stable git English), no prompt/no net.
  Guard `TESTENV-HERMETIC`: pins an **absolute golden base SHA** (`48f4258c…`, sha1) — fully
  determined by identity+dates+content+message — plus injected author identity + symlink-normalized
  root. Mutants confirmed: drop fixed dates → SHA ≠ golden RED; drop `GIT_AUTHOR_NAME` → ambient
  username (`admin`) leaks → identity RED. **Note:** a relative "two runs agree" determinism check
  was rejected as vacuous (same-second SHA collision); the absolute golden is the real pin. `RunWI`
  deferred to M3 (needs the built binary).

- **M0 · `internal/lockfs` flock half** — `flock_unix.go` (`//go:build unix`) + `flock_unix_test.go`:
  `FileLock`, the advisory whole-file `flock(2)` lock that serializes concurrent `wi` processes
  touching the same `.wi/` resource (DESIGN §6.1). `NewFileLock(path)` + `TryLock()` (non-blocking,
  `(bool,err)`) / `Lock()` (blocking) / `Unlock()` (idempotent no-op when not held); double-lock of a
  held handle is a usage error. Built on `syscall.Flock(LOCK_EX|LOCK_NB)` — **decision #6 flock leg
  RESOLVED: hand-rolled on stdlib `syscall.Flock`, NOT `gofrs/flock`** (zero new deps, INV-NO-LLM
  stays trivially green; PID/self-heal layer is hand-written regardless). Kernel releases the lock on
  process exit, so a crashed holder never wedges it. Guard `FLOCK-EXCLUDES` exploits BSD flock's
  per-open-file-description semantics to prove exclusion in-process (two independent handles contend):
  TryLock refuses a second holder + frees on Unlock; blocking Lock waits then proceeds after release.
  Mutant (`LOCK_EX`→`LOCK_SH`) confirmed `TestFlockExcludesSecondHolder` + `TestLockBlocksUntilReleased`
  RED (shared locks coexist). Reverted → GREEN. Auto-lock-break self-heal (§7.3) is a separate M4 unit.

- **M0 · `internal/layout` Bootstrap + Resolve** — `layout.go` + `bootstrap_test.go`: the two
  filesystem-aware constructors completing the layout package. `Resolve(root)` is the
  EvalSymlinks-normalized constructor the CLI uses at startup (DESIGN §4) — requires an existing
  absolute root, resolves every symlink so the canonical root is a fixed point (matters on macOS:
  /var → /private/var). `(l Layout) Bootstrap()` materializes the `.wi/` runtime subtree (WiDir + all
  7 `WiSubdirs`) and writes a self-ignoring `.wi/.gitignore` (`*\n`) so runtime state can never be
  committed (DESIGN §1) without wi touching the user's ignore files; idempotent. **First real consumer
  of `lockfs.WriteFileAtomic`** — dogfoods the §6.2 single-writer invariant. Guard `LAYOUT-BOOTSTRAP`:
  symlinked-root resolution + fixed-point, relative/missing-root rejection, subtree+gitignore creation,
  idempotency. Mutants confirmed: skip the WiSubdirs loop → `TestBootstrapCreatesSubtree` RED
  (`.wi/locks` missing); drop EvalSymlinks (`return New(root)`) → `TestResolveNormalizesSymlinks` RED
  (link unresolved) **and** the missing-root reject RED (proving the existence guard comes from
  EvalSymlinks). Reverted → GREEN. **`internal/layout` is now complete** (path core + Bootstrap +
  Resolve).

- **M0 · `internal/lockfs` atomic writer** — `atomic.go` + `atomic_test.go`: `WriteFileAtomic(path,
  data, perm)`, the SINGLE atomic writer every `.wi/` state writer reuses (DESIGN §6.2). Recipe:
  `os.CreateTemp` in the SAME dir (rename stays intra-fs ⇒ atomic) → write → fsync → chmod to the
  caller's mode (CreateTemp gives 0600; Chmod dodges umask) → close → `os.Rename` over the target →
  `fsyncDir` the parent so the rename itself is durable. Failure paths remove the temp and leave the
  target untouched. **First consumer of the `WI_FAULT` seam:** `FaultBeforeRename`
  (`lockfs.before_rename`) aborts in the exact temp-written-but-not-renamed window. Guard
  `HEAL-ATOMIC-WRITE` (plain `t.TempDir`, no git ⇒ no `testenv` needed): content+perm round-trip;
  **crash-safety** — under the injected fault the target keeps its complete OLD content (never torn)
  and no temp turds remain; two-sided floor that an un-faulted replace DOES apply. Mutant (in-place
  `O_TRUNC` write instead of temp+rename) confirmed `TestAtomicReplaceIsCrashSafe` RED (target torn to
  `"v2…"`), reverted → GREEN. flock advisory-lock half is a separate follow-up unit.

- **M0 · `internal/lock` (lock-key namespace + total-order acquire)** — `keys.go` + `acquire.go`
  (+ `keys_test.go`, `acquire_test.go`): the SOLE owner of wi's advisory lock-key namespace, built on
  `lockfs.FileLock` (DESIGN §6.1). Closed namespace: `project-registry`, `repo:<name>` (the key sync
  AND land both take — this is what linearizes their freshness race), `isolate-state:<task>`.
  Constructors `ProjectRegistry()`/`Repo(name)`/`IsolateState(task)` route names through the new
  exported `layout.ValidateSegment` (one shared traversal chokepoint — keys become lock filenames), and
  a `Key` derives its own `.lock` path so callers never assemble lock paths. `Acquire(locksDir, keys…)`
  folds the set into `orderedUnique` order (sorted+deduped) and TryLocks each non-blocking:
  all-or-nothing — a held key rolls back the partial acquire and returns typed `*HeldError` (→ exit 6
  `lock_held`), never blocks, never double-grants; `Held.Release()` frees in reverse, idempotent.
  Guards: `LOCK-KEYS` (pinned canonical strings + path derivation + unsafe-name rejection),
  `LOCK-ORDER` (`orderedUnique` is order-independent + dedups), `LOCK-MUTEX` (overlap refused with
  `*HeldError` naming the key; partial-failure rollback proven via fresh re-acquire). All four mutants
  confirmed RED: no-op sort comparator → `TestOrderedUnique…` RED; treat refused TryLock as success →
  `TestAcquireRefusesOverlap` RED; skip rollback on refusal → `TestAcquireReleasesOnPartialFailure` RED;
  `repo:`→`repository:` → `TestCanonicalKeyStrings` RED. Reverted → GREEN. Auto-lock-break self-heal
  (§7.3) stays a separate M4 unit. **M0 building blocks are now complete** (contract spine + layout +
  opid + clock + testenv + lockfs + lock).

- **M1 · `internal/gitexec` (runner + egress belt)** — `gitexec.go` + `gitexec_test.go`: the single
  chokepoint that launches every git child process (DESIGN §4, §2.3). `Runner` (New = inherit env;
  NewWithEnv = explicit hermetic base) with `Run` (OFFLINE — overlays `GIT_ALLOW_PROTOCOL=none` so git
  refuses every transport, the no-hidden-network belt; + `GIT_TERMINAL_PROMPT=0`) and `RunNetwork` (the
  narrow online opt-in for fetch/clone, prompt still disabled). Captures `Result{Args,Stdout,Stderr,
  ExitCode}`; non-zero exit → typed `*ExitError` carrying the full Result (so the later stderr→kind
  classifier reads it without re-running git); start failure → plain wrapped error. `setEnv` overlays
  keys by replace-not-append so git never sees a duplicate key. testenv gained `GitEnv()` to feed the
  hermetic base. Guards: `GITEXEC-OFFLINE-BELT` (two-sided, fully hermetic via a local `file://` remote:
  `ls-remote` is REFUSED with "transport 'file' not allowed" through `Run` but SUCCEEDS through
  `RunNetwork`, so the refusal is attributable to the belt) + `GITEXEC-CAPTURE` (stdout floor via `git
  version`; exit-error surfaced for an unknown subcommand). Mutants confirmed: drop the belt → offline
  `ls-remote` succeeds → `TestOfflineRefusesTransport` RED; swallow the non-zero exit → `TestRunSurfaces
  ExitError` RED. Reverted → GREEN. **Note:** this is the unit-level half of `INV-NO-NETWORK`; the
  module-wide architecture test (git-child belt asserted across all offline command paths) lands later
  in `internal/invariants`.

- **M1 · `internal/git` (SSOT keystone)** — `git.go` + `git_test.go`: deterministic typed git verbs
  on `gitexec.Runner`, all local (offline `Run`, never dial). `ResolveRef(dir, ref)` reads a verified
  commit SHA (`rev-parse --verify --end-of-options`). **`FastForwardBaseRef(dir, base, newRev)` is the
  SOLE base-ref-mutation path (DESIGN §5):** reads current `refs/heads/<base>`, checks `merge-base
  --is-ancestor <cur> <new>` (exit 1 ⇒ genuine non-ff ⇒ typed `*NonFastForwardError`, ref untouched;
  other exits ⇒ real error), then `update-ref refs/heads/<base> <new> <cur>` — **no checkout, no
  merge**, with the old value asserted so a concurrent change fails atomically rather than racing. Works
  on the detached-HEAD SSOT; v0 sync + v1 land both reuse it. Guard `GIT-FF-ONLY` (two-sided, via a
  `testenv` SSOT): a true fast-forward advances the ref; a divergent sibling commit is REFUSED with the
  ref SHA unchanged (before==after). Mutant (drop the `--is-ancestor` precheck → unconditional
  update-ref) confirmed `TestFastForwardRefusesNonFastForward` RED (divergent target advances, no
  error). Reverted → GREEN.

- **M1 · `internal/git` · `EnsureClone` + SSOT-pristine predicate** — `git.go` + `git_test.go`:
  completes the SSOT clone lifecycle on top of the keystone. `EnsureClone(dir, originURL, base)` lazily
  materializes an absent clone in the SSOT posture — `clone --branch <base>` (via the network-only
  `gitexec.RunNetwork`, the ONE permitted dial) then a local `switch --detach`, so refs/heads/<base>
  exists at the origin's base tip but is NOT checked out (advancing it via update-ref never touches a
  working tree). Idempotent: an existing repo (guarded `os.Stat` dir + `rev-parse --git-dir`) is a noop
  — no re-clone, no network. `StatusPorcelain`/`IsClean` are the SSOT-pristine check — `git status
  --porcelain`, clean iff empty, so even an UNTRACKED turd counts as drift. Guards: `GIT-CLONE-DETACHED`
  (fresh clone → HEAD abbrev-ref is `"HEAD"` AND HEAD == refs/heads/<base> tip; a sentinel file survives
  a second call, proving no re-clone) + `GIT-CLEAN` (two-sided: fresh clone clean, one untracked file
  dirty). Mutants confirmed: skip the `switch --detach` → HEAD abbrev-ref is `"main"` →
  `TestEnsureCloneDetachesAtBaseTip` RED; make `IsClean` always return true →
  `TestIsCleanReflectsWorkingTree` RED on the dirtied case. Reverted → GREEN. **`internal/git` is now
  complete** (ResolveRef / FastForwardBaseRef / EnsureClone / StatusPorcelain / IsClean).

- **M1 · `internal/mirror` (cached freshness — read/classify)** — `mirror.go` + `mirror_test.go`: the
  cached SSOT-freshness layer that feeds `mirror_freshness` in the envelope (DESIGN §5). `Snapshot`
  (comparable, all-scalar) records what the last fetch observed — `repo`/`base`/`fetched_at`/
  `local_base_sha`/`origin_base_sha`/`behind_origin_as_of_fetch` — persisted to
  `<root>/.wi/mirrors/<repo>.json` via the single atomic writer (`lockfs.WriteFileAtomic`, DESIGN §6.2)
  and read back by `Load` with **zero** network (the package imports no git/gitexec and takes no Runner,
  so a read structurally cannot dial). `Snapshot.Freshness()` projects onto `contract.MirrorFreshness`
  purely (no I/O): **`stale = behind_origin_as_of_fetch > 0`** (decision #M below). A never-fetched repo
  → `ErrNoSnapshot` so callers omit the block (≠ stale). Repo name (→ filename) routes through the
  shared `layout.ValidateSegment` chokepoint, mirroring `lock`'s `<key>.lock`-in-LocksDir pattern.
  Guards: `MIRROR-FRESHNESS` (two-sided: behind>0 stale, behind==0 fresh, carrying count + fetched_at)
  + `MIRROR-PERSIST` (Store→Load round-trip, missing→`ErrNoSnapshot`, traversing name rejected).
  Mutants confirmed: `Stale:false` hardcode → `TestFreshnessClassifiesStaleByBehindCount` RED; Store
  diverts the write (`p+".mutant"`, import kept used) → `TestSnapshotRoundTrips` RED (Load can't find
  it). Reverted → GREEN.

- **M1 · `internal/git` · `Fetch` + `DivergedCounts` (the freshness git verbs)** — `git.go` +
  `git_test.go`: the two raw git verbs the upcoming `mirror.Fetch` orchestration composes.
  **`Fetch(dir, remote)`** is the SECOND (with `EnsureClone`) network-permitted verb — it routes through
  `gitexec.RunNetwork` and updates `refs/remotes/<remote>/*` only; it never moves a local branch ref and
  never touches the working tree (advancing the SSOT base stays `FastForwardBaseRef`'s exclusive job,
  DESIGN §5). **`DivergedCounts(dir, local, remote)`** reads `rev-list --left-right --count
  local...remote` from LOCAL refs only (offline) → `(ahead, behind)`, the basis for both freshness
  (behind) and the future `main_state` classification. Guards (shared `fetchedMirror` helper: origin at
  C0, EnsureClone'd mirror, push child C1 to origin, fetch): `GIT-FETCH` (post-fetch
  `refs/remotes/origin/<base>` == C1 while `refs/heads/<base>` stays C0 AND `IsClean` true) +
  `GIT-DIVERGED` (local-vs-origin ahead 0/behind 1; reversed args flip to ahead 1/behind 0 — pins each
  count to the right column). Mutants confirmed: no-op `Fetch` (skip the dial) → tracking ref stays C0 →
  `TestFetchAdvancesRemoteTrackingOnly` RED; swap the rev-list columns → `TestDivergedCountsAheadBehind`
  RED (and `GIT-FETCH` stays green, so the swap is attributable to `DivergedCounts`). Reverted → GREEN.
  The Git-struct doc now names `EnsureClone`+`Fetch` as the only two dialing verbs.

- **M1 · `internal/mirror` · `Refresh` (the fetch orchestration)** — `fetch.go` + `fetch_test.go`:
  the one network step of the freshness layer, composing the git verbs. `Refresh(ctx, g, clk,
  mirrorsDir, repo, dir, base)`: `g.Fetch(dir, "origin")` (the dial), resolve `refs/heads/<base>`
  (local_base_sha) + `refs/remotes/origin/<base>` (origin_base_sha), `g.DivergedCounts` behind column,
  build `Snapshot{FetchedAt: clk.Now().UTC() RFC3339, ...}`, `Store`, return it. It is the ONLY part of
  `mirror` that touches git/network, so it takes `*git.Git`+`clock.Clock`; placed in a SEPARATE
  `fetch.go` so `mirror.go`'s "this file imports no git" doc stays literally true (the read path
  Load/Freshness is still Runner-free and cannot dial). Refresh does NOT advance the base ref — only the
  remote-tracking ref moves, so the SSOT tree stays pristine. Guard `MIRROR-FETCH` (testenv origin at
  C0, EnsureClone'd mirror, push C1 to origin, Refresh): behind==1 so `Freshness().Stale`, origin_base
  == C1, local_base == C0 (unmoved), fetched_at == injected fake-clock instant, `git.IsClean` holds, and
  the returned snapshot equals what `Load` reads back. Mutant (skip `g.Fetch`, classify against the
  stale tracking ref) confirmed RED on behind/stale/origin_base, reverted to GREEN. **`internal/mirror`
  is now complete** (Snapshot/Freshness/Store/Load read+classify + Refresh fetch).

- **M1 · `internal/invariants` · `INV-NO-NETWORK` (module-wide architecture test) — CLOSES M1** —
  `nonetwork_test.go`: the architecture half of DESIGN §2 #3 (the gitexec `GITEXEC-OFFLINE-BELT` unit
  proves the belt *works*; this proves the belt *cannot be bypassed*). `TestNoHiddenNetwork` walks the
  module tree (skipping `.git`, non-`.go`, and `_test.go`), derives each file's slash-separated package
  path, and — for every package NOT in `egressAllowed` — fails if it imports `os/exec` (can spawn a git
  child) or references the `GIT_ALLOW_PROTOCOL` belt key. Uses **go/parser** (not grep), so the belt key
  in a comment or this guard's own prose never false-positives; pure `scanFileForEgress(src)
  (importsExec, refsBelt)` is driven directly by the non-vacuity test. **Allowlist decision (#N below):**
  `{internal/gitexec, internal/testenv}` — gitexec is the runtime chokepoint that applies the belt;
  testenv is the test-only git-fixture harness (a non-`_test.go` support pkg never reachable from
  `cmd/wi`). Survey confirmed those are the only two source files importing `os/exec`, and
  `GIT_ALLOW_PROTOCOL` appears only in gitexec. Guard `INV-NO-NETWORK` + self-test
  `TestNoNetworkScannerIsNonVacuous` (scanner flags an `os/exec`-import source, flags a belt-key
  string-literal source, and clears a clean source). **Two mutants demonstrated** (arch tests co-locate
  detector+test, so RED-first is the mutate-demonstrate cycle, per the INV-NO-LLM precedent): empty
  `egressAllowed` → `TestNoHiddenNetwork` RED (gitexec+testenv themselves trip, proving the walk + both
  detectors fire on real source); `scanFileForEgress` always `(false,false)` → `TestNoNetworkScanner
  IsNonVacuous` RED (a blind scanner is a silent false negative). Both reverted → full `go build/vet/test`
  GREEN. **M1 is now COMPLETE** (gitexec belt + full git verbs + mirror + INV-NO-NETWORK).

- **M2 · `internal/config` (manifest read + validate) — M2 BEGINS** — `config.go` + `config_test.go`:
  the SOLE owner of the committed manifest `<root>/wi.config.jsonc` (DESIGN §1 line 19, §map line 167),
  read+validate half. `Parse([]byte) (Config, error)` strips JSONC comments then decodes with
  `encoding/json` under `DisallowUnknownFields` (closed key set — unknown key at ANY level is a hard
  error), requires exactly one JSON value (`dec.More()` guard), and validates each repo: non-empty
  `name` routed through the shared `layout.ValidateSegment("repo", …)` traversal chokepoint (names become
  `repos/<name>` segments), non-empty `url`, and an **effective base** = the repo's own `base` else
  `defaults.base` (a repo with neither is rejected); duplicate names rejected. `Load(path)` wraps
  `os.ReadFile`+`Parse` and surfaces a missing file as `fs.ErrNotExist` (so the CLI can branch to suggest
  `wi init`). Resolved `Config{Defaults, Repos}` exposes effective bases so downstream never re-applies
  the default; `Config.Lookup(name)`. `stripJSONC` is a hand-rolled state machine (normal/string/line-
  comment/block-comment, honoring `\\` escapes so a `//` inside a JSON string survives, preserving
  newlines for decoder error positions). Guard `CONFIG-PARSE`: golden manifest (comments + an inherited
  base + an explicit base) → expected typed `Config`; an 11-case reject corpus (unknown key at
  top/defaults/repo level, missing name/url, no-base-anywhere, duplicate, traversing name, malformed
  JSON, trailing content, comments-only); empty-manifest accept floor (`{"repos":[]}` and `{}`); `Load`
  round-trip + missing→`fs.ErrNotExist`. **Two mutants demonstrated:** `stripJSONC`→no-op (`return src`)
  → comment becomes a JSON syntax error → `TestParseAcceptsGolden` RED; drop `DisallowUnknownFields` →
  all 3 unknown-key cases parse cleanly → `TestParseRejectsInvalid` RED. (The "unknown repo key" case was
  strengthened with a valid `base` so it isolates `DisallowUnknownFields` rather than tripping the
  missing-base rule.) Both reverted → full `go build/vet/test` GREEN. **Decision #C** (JSONC parser =
  hand-rolled stripper + stdlib, zero new deps) recorded below. The AST-preserving *edit* path (for
  `repo add`) and trailing-comma tolerance are deferred to the writer unit.

- **M2 · `internal/state` (runtime registry — per-isolate record)** — `state.go` + `state_test.go`:
  the SOLE owner of the `.wi/state/` runtime registry (DESIGN §map line 168). `IsolateRecord{Task, OpID,
  Repos []RepoRecord}` with `RepoRecord{Repo, Stage}` is the durable entry for one isolate;
  `NewIsolateRecord(task, opID, repos)` builds every declared repo at `StagePending`. Persistence mirrors
  `internal/mirror` exactly: flat `<stateDir>/<task>.json`, task name routed through the shared
  `layout.ValidateSegment("task", …)` traversal chokepoint, `Store` = `json.MarshalIndent`+`\n`+the single
  atomic writer `lockfs.WriteFileAtomic` (§6.2), `Load` with `fs.ErrNotExist`→`ErrNoRecord` (the
  "isolate never created" sentinel, like mirror's `ErrNoSnapshot`). **`UpdateRepoStage(stateDir, task,
  repo, stage)`** is the durable-partial-success operation (DESIGN §6.3): load → flip exactly the named
  repo's stage (unknown repo is an error) → atomic re-store, called **after each worktree add** so a crash
  mid-multi-repo leaves a registry reflecting EXACTLY the completed repos. Takes no Runner and dials
  nothing (pure local persistence, like mirror); the caller holds the `isolate-state:<task>` lock around
  the load-modify-store. Guards: `STATE-PERSIST` (Store/Load round-trip with all-pending fresh start,
  missing→`ErrNoRecord`, unsafe task name rejected, `UpdateRepoStage` flips one repo + errors on unknown
  repo, flat-`<task>.json` path) + `STATE-DURABLE` (an `UpdateRepoStage` interrupted in the atomic
  writer's pre-rename crash window via `WI_FAULT=lockfs.before_rename` MUST fail and leave the PRIOR
  durable record intact — the completed flip survives, the interrupted one neither applies nor tears the
  file). **Three mutants demonstrated:** `Store` diverts the write (`p+".mutant"`) → `Load` can't find it
  → `TestRecordRoundTrips` RED; `UpdateRepoStage` skips its unknown-repo error (`found := true`) →
  flipping a non-existent repo wrongly succeeds → `TestUpdateRepoStageFlipsOneRepo` RED; `Store` swaps the
  atomic writer for `os.WriteFile` (lockfs kept referenced so the *assertion* reddens, not the compiler) →
  the injected pre-rename crash no longer aborts so the interrupted flip lands → `TestDurablePartialSuccess`
  RED. All reverted → full `go build/vet/test` GREEN. **Decision #S** (Stage is a state-owned typed string,
  not a contract enum) recorded below.

- **M2 · `internal/git` · `AddWorktree` (isolate materialization primitive)** — `git.go` +
  `git_test.go`: the per-repo verb `internal/isolate` composes to materialize one worktree off the SSOT.
  `AddWorktree(ctx, ssotDir, worktreePath, rev)` runs `git worktree add --detach <path> <rev>` (offline
  `Run`), producing a **linked worktree sharing the SSOT object store** (native git sharing — no object
  duplication, DESIGN §1 line 30) that is **detached** (holds no branch, so the SSOT base ref is never
  "checked out in a worktree" and `FastForwardBaseRef` can always advance it — the keystone, DESIGN §5).
  rev is wi-internal (a SHA or `refs/heads/<base>`); ownership/gc-protection via the
  `refs/wi/owned/<task>/<repo>` marker (DESIGN §7.1) is a separate follow-on step. Guard `GIT-WORKTREE`
  (testenv SSOT, EnsureClone'd): after add, the worktree HEAD is detached (`--abbrev-ref` == "HEAD") at
  the base tip; its `.git` is a **gitlink file** (not a dir) and `rev-parse --git-common-dir` resolves to
  the **SSOT's own `.git`** (structural object-store-sharing / no-dup check, the isolation invariant PLAN
  §line 102); and the SSOT working tree stays **pristine** (`IsClean`). Mutant (materialize via a
  standalone `git clone` instead of a linked worktree) confirmed RED on **all three** assertions —
  abbrev-ref "main" (branch checked out, not detached), `.git` a directory, and the common git dir the
  clone's own (object duplication) — proving the guard verifies genuine worktree sharing, the precise
  worktree-vs-clone design choice, not merely "a checkout appeared". Reverted → full `go build/vet/test`
  GREEN. (`--detach` is defense-in-depth: a SHA or fully-qualified ref already detaches; the flag keeps a
  short-branch-name caller detached too.)

- **M2 · `internal/git` · `CreateOwnedRef` + `OwnedRefSHA` (evidence-positive ownership marker)** —
  `git.go` + `git_test.go`: the wi-owned marker-ref verbs (decision #2, DESIGN §7.1), the second
  sub-step of `internal/isolate`. **`CreateOwnedRef(ctx, ssotDir, task, repo, sha)`** atomically writes
  `refs/wi/owned/<task>/<repo>` at sha (a single `update-ref`) — the POSITIVE evidence reclamation
  requires: a worktree/branch is reclaimable only if such a marker proves wi created it; an unexplained
  orphan with no marker is a HARD BLOCK, never auto-pruned. **`OwnedRefSHA(...)` → `(sha, exists, err)`**
  reads it back via `rev-parse --verify --quiet`, cleanly distinguishing a genuinely absent marker
  (`exists=false`, nil error — the "no ownership recorded" case reclamation inspects on an orphan) from a
  real read failure (exit 1 + empty output ⇒ absent; the same `*gitexec.ExitError` exit-code idiom
  `FastForwardBaseRef` uses). The namespace lives in one place (`ownedRef`), exactly as
  `FastForwardBaseRef` owns `"refs/heads/"+base`; task/repo are caller-validated (this package holds no
  path policy). The decisive property: markers live under `refs/wi/*`, NOT `refs/heads/*`, so the commit
  is gc-reachable yet the marker is **not a branch** — the pristine SSOT never grows a stray branch
  (DESIGN §5). Guard `GIT-OWNED-REF` (testenv SSOT, EnsureClone'd): absent-before via the verb; after
  create the verb reads back the sha AND raw git confirms `refs/wi/owned/<task>/<repo>` == sha while
  `refs/heads/` still holds ONLY the base ref (no leaked branch). Mutant (flip `ownedRef`'s namespace
  `refs/wi/`→`refs/heads/`) confirmed RED on both the "lives under refs/wi at the sha" and the "no stray
  branch" assertions (refs/wi/owned empty; refs/heads/ grew `wi/owned/taskx/acme`), while the round-trip
  stayed GREEN — proving those two assertions, not the round-trip, carry the decision-#2 namespace
  property. Reverted → full `go build/vet/test` GREEN. **Decision #2** (git ref over note/reflog AND over
  a `.wi/index` backref) recorded below + marked RESOLVED in PLAN §7.

- **M2 · `internal/isolate` · `isolate.New` (the N-repo orchestration — durable partial success)** —
  `isolate.go` + `isolate_test.go`: the domain core of `wi isolate new`, the partial-success-critical
  command (DESIGN §6.3). `New(ctx, l, g, task, opID, specs)` acquires the `isolate-state:<task>` lock
  (held → `*lock.HeldError` → exit 6), `mkdir`s `isolas/<task>/`, writes a `state.NewIsolateRecord` with
  every repo `StagePending` **before any materialization** (the durable statement of intent that makes
  the op resumable), then materializes repos in request order. Each repo, in the exact evidence-positive
  order: `AddWorktree --detach` off `refs/heads/<base>` → `CreateOwnedRef` (marker BEFORE claiming
  "created", so a crash leaves a wi-owned reclaimable worktree, never an unexplained orphan, §7.1) →
  `state.UpdateRepoStage(…Created)`. **Stop-on-first-fail with durable, NOT-rolled-back completed repos:**
  the first failing repo halts the run, repos before it stay on disk + in the registry, repos after it are
  never attempted (stay `StagePending`); the result carries `StatusPartial` (→ exit 2) and the registry
  reflects EXACTLY the completed set. A per-repo failure is recorded in the `Result` (not a Go error);
  `New`'s error return is reserved for can't-run-at-all (held lock, unwritable initial record). Decoupled
  from the manifest via `isolate.RepoSpec{Name, Base}` (the CLI maps `config.Repo`→`RepoSpec`). Never
  moves a base ref / never dirties the SSOT (DESIGN §5). Guard `ISOLATE-NEW` (testenv SSOTs): (complete)
  3 repos all materialize detached, each marker records the worktree tip, durable record all `created`,
  SSOT base refs unmoved; (partial — the core) "web" has no SSOT clone so its add fails → "api" before it
  stays `created` with a durable worktree + pristine SSOT, "db" after it stays `pending` with no worktree,
  `Status==partial`, durable registry == exactly {api:created, web:pending, db:pending}; (lock) a
  pre-held `isolate-state:feat` makes `New` return `*lock.HeldError`. **Two mutants demonstrated:** drop
  the stop-on-first-fail `return` (→ `continue`) so the loop materializes "db" past the failed "web" →
  reddens exactly the 3 "db not attempted" assertions (result stage, durable stage, on-disk worktree),
  api/web/Status staying green — isolating the §6.3 stop-on-first-fail / "exactly the completed set"
  property; skip the upfront all-pending `state.Store` → the first repo's `UpdateRepoStage` errors
  `state: no isolate record` and there's no durable registry to resume from → reddens both the complete
  and partial guards (proving the durable-intent write is load-bearing). Both reverted → full
  `go build/vet/test` GREEN. **Deferred:** `isolate.New` resume (on re-run, skip repos already
  `StageCreated` rather than re-adding and failing) is a small follow-on once `resolve`/CLI land.

- **M2 · `internal/resolve` · `Bundle` (the path-bundle projector) — COMPLETES M2** — `resolve.go` +
  `resolve_test.go`: the data behind `wi resolve <task>` (and the `resolve` block isolate responses embed,
  DESIGN §3.1, §map line 166). `Bundle(l, rec)` is a **PURE projection** of a `layout.Layout` + a
  persisted `state.IsolateRecord` — **zero I/O** (no FS reads, no git, no network — stronger than mirror's
  offline read path), so it is trivially offline. Every path comes from `internal/layout` (the sole path
  owner — resolve assembles nothing): `isolate_root`=`TaskDir` (`isolas/<task>`), `state_dir`=`StateDir`
  (`.wi/state`), `log`=`LogDir` (`.wi/log`); per repo (iterating `rec.Repos`, the isolate's actual
  contents, in recorded order) `worktree`=`Isolate` (`isolas/<task>/<repo>`) and `mirror`=`Repo`
  (`repos/<repo>`, the SSOT clone / local mirror of origin). The CLI owns `state.Load` + the
  `ErrNoRecord`→`not_found` mapping; `Bundle` takes the loaded record so it stays a total, testable
  function. Guard `RESOLVE-BUNDLE`: a 2-repo record projects to the exact hand-written golden paths
  (built independently of the layout accessors, so a mis-wire is caught), in order; an empty record yields
  a non-nil empty `Repos` (marshals as `[]`); a traversing repo name surfaces as a validation error.
  **Two mutants demonstrated:** wire `mirror` to the worktree path instead of `layout.Repo` → both repos'
  Mirror reddens (proves resolve distinguishes the `isolas/<task>/<repo>` worktree from the `repos/<repo>`
  SSOT clone); drop a repo from the loop → count/second-repo reddens (proves every recorded repo is
  projected). Both reverted → full `go build/vet/test` GREEN. **Decision #R** (resolve field semantics;
  v0 `branch` empty because worktrees are detached) recorded below. **M2 is now COMPLETE** (config +
  state + isolate + resolve); next is M3 — the CLI surface.

## Next unit (pick this on the next firing) — M3 BEGINS (the CLI surface → MVP)

M3 (DESIGN §3, IMPLEMENTATION_PLAN §M3 + Wave B) wires the green domain core through the uniform
pipeline into the runnable `wi` binary: `internal/cli` (parse → dispatch → **one** envelope out →
mapped exit), `help`, `suggest`, then `cmd/wi`, with CI + `.goreleaser.yaml` + Homebrew tap. The hard
part is the contract plumbing (one well-formed envelope per invocation, JSON default, the closed
exit-code set, text as a lossless projection), NOT arg parsing — so build the contract spine of the
CLI first, bottom-up, smallest cohesive unit each firing.

Done so far in M3 (bottom-up): `exitcontract` (the `error.kind→exit-code` matrix `ExitCodeFor` + the
sole `os.Exit` wrapper, `SHAPE-FAIL-MATRIX`), `cli.Emit` (one-envelope serialization, `SHAPE-ONE-ENVELOPE`),
the `cli` ASSEMBLER (`Meta` + `Success`/`Failure` + `ExitFor`, `SHAPE-ASSEMBLE`/`SHAPE-DRYRUN-EXIT0`),
`cli.RenderText` (the `--format text` lossless projection, `SHAPE-TEXT-PROJECTION`), the Runner
EXECUTE-CORE (`Result`/`Command`/`CommandError` + `Format`/`Render` + `Execute`→`envelopeFor`→`ExitFor`,
`RUN-PIPELINE`), and now the DISPATCH layer (`Registry` + `Dispatch`: argv → globals + longest-match
command → `op_id` mint → `Meta` → `Execute`, `DISPATCH-ROUTES`, decision #F = hand-rolled stdlib not
cobra). **The entire generic CLI pipeline — argv → dispatch → outcome → one envelope (json|text) →
mapped exit — is now complete and green.** What remains for MVP is the per-command handlers that plug
real domain work into that pipeline, then the `cmd/wi` main, then CI/release.

- **DONE (this iteration):** `cmd_sync.go` — the **`wi sync [<repo>…]` handler** (guard `CMD-SYNC`), the
  thin envelope projection over the now-green `sync.Run` core. Added a `Clock` field to `Deps` (sync is the
  first handler that timestamps a snapshot) + the `"sync"` `BuildRegistry` line binding
  `d.Layout`/`d.Git`/`d.Clock`. `newSyncCommand` does NO arg validation (no operands = all declared repos;
  named operands checked against the manifest in `Run`). `Run` loads the manifest (missing →
  not_found+`wi init`, malformed → usage — decision #H), `selectRepos` resolves operands (none → every
  `cfg.Repo` in declaration order; first undeclared → not_found before any dial), maps to
  `[]sync.RepoSpec{Name,URL,Base}` (proving the manifest URL is read — isolate new ignores it), drives
  `sync.Run`, and projects each outcome → `RepoResult{Action:synced, Branch, SHA:Snapshot.LocalBaseSHA,
  Mirror, Freshness}` with `StatusPartial`→durable `(result, *CommandError{partial, Action:synced})`
  mirroring `cmd_isolate_new` (#D). **Per-repo-kind question settled:** every per-repo failure projects to
  `kind=internal` for now (matching `projectRepoOutcome`'s precedent), with typed refinement
  (`*git.NonFastForwardError`→conflict, `*lock.HeldError`→lock_held) DEFERRED to the gitexec stderr→kind
  classifier so both sibling projections gain it uniformly rather than diverging — see decision #K.
- **DONE (prior iteration, this run):** `internal/sync` — the **sync domain core** `sync.Run` (guard
  `SYNC-RUN`). Built as its own domain package (mirroring `internal/isolate`) rather than inline in the
  handler, so the orchestration — per-repo, under `repo:<name>`,
  `EnsureClone`→`Fetch`→`FastForwardBaseRef`→`mirror.Store`, CONTINUE-on-fail — is hermetically testable
  below the envelope machinery. The first `internal/` package to drive `gitexec.RunNetwork` (clone+fetch)
  end-to-end.
- **DONE (prior iteration):** `isolate new <task> <repo>…` — the marquee handler — guard
  `CMD-ISOLATE-NEW`; added `Deps.Git`. Resolves each requested repo against the manifest → `RepoSpec`
  (undeclared → not_found, missing manifest → not_found+`wi init`, malformed → usage = decision #H),
  reads the op_id via `OpIDFrom(ctx)` into the durable `IsolateRecord`, drives `isolate.New`, and maps
  `StatusComplete`→created / `StatusPartial`→durable `(result, *CommandError{partial})` (#D) /
  `*lock.HeldError`→lock_held.
- **DONE (prior iteration):** the `WithOpID`/`OpIDFrom` **context seam** (`Execute` injects `Meta.OpID`
  into the ctx the Command sees) — guard `CTX-OPID`. The prerequisite that let `isolate new` write the
  envelope's op_id into the durable `IsolateRecord` instead of a divergent one.
- **DONE (prior iteration):** `init` (scaffold a workspace) — guard `CMD-INIT`; resolves decision #G
  (root = cwd, init takes no operand). Bootstraps `.wi/` then writes a starter `wi.config.jsonc` LAST
  (O_EXCL commit point); re-init → `already_exists` leaving the manifest byte-for-byte intact.
- **DONE (prior iteration):** `resolve` (pure read) + the `Deps`/`BuildRegistry` seam — guard
  `CMD-RESOLVE`. The handler→`Result`/`CommandError` contract pattern is now established for the rest.
- **NEXT — M3 · the remaining per-command handlers, one cohesive unit each** (`sync` →
  `repo add` → `isolate rm`). Each is a `Command` (`Run(ctx) (*Result, error)`) doing its
  domain work over the green M0–M2 core (config/state/git/mirror/isolate) and returning a typed
  `*Result` (or `*CommandError`) — NEVER an envelope, never an exit code (the pipeline owns both) — plus
  one line in `BuildRegistry` and a factory that parses+validates its args (→ `*CommandError{Kind:usage}`
  → exit 64). Guard each with its own fitness test asserting ONLY the domain mapping (the generic
  `RUN-PIPELINE`/`DISPATCH-ROUTES` already prove the envelope/exit wiring), following
  `CMD-RESOLVE`/`CMD-INIT`/`CMD-ISOLATE-NEW`.
  **Suggested next: the `cmd_repo_add.go` handler** (`wi repo add <name> <url> [--base <branch>]`) — the
  unit where the deferred **AST-preserving config edit path** finally lands. Plan: a new
  `config.Add(path, Repo)` (or `config`-package editor) that READS `wi.config.jsonc`, inserts the new repo
  object into the `repos` array, and ATOMICALLY rewrites the file **preserving comments and formatting**
  (the hand-rolled JSONC stripper is read-only — the edit path must NOT round-trip through
  `encoding/json`, which would drop comments; insert textually into the existing byte stream). Take the
  `project-registry` lock for the duration (registry mutation, serializes concurrent `repo add`s). Refuse
  a duplicate name → `already_exists`; a missing manifest → not_found+`wi init`; a malformed manifest →
  usage. The factory validates `<name>` through `layout.ValidateSegment` (a path segment) at parse time and
  requires exactly `<name> <url>` (+ optional `--base`). Project onto `Result{Action:created, …}` (no
  network, no repos[]). Guard `CMD-REPO-ADD` asserts: a clean add appends + preserves a sentinel comment;
  a duplicate name → `already_exists`; the manifest is byte-stable except the inserted block. Then
  `isolate rm` (evidence-positive reclamation via `refs/wi/owned/<task>/<repo>` — HARD BLOCK on an
  unexplained orphan, never auto-prune; DESIGN §7.1).
- **Then** `cmd/wi/main.go` — the single process entry: discover the root → build `Deps` +
  `clock.System`, `BuildRegistry`, call `cli.Dispatch`, and make the SOLE `os.Exit` via
  `exitcontract.Exit(code)`. Then CI + `.goreleaser.yaml` + Homebrew tap. Completing this chain = full
  MVP (M0–M3) green = a STOP condition.
- Deferred follow-ons (pull in when a command drives them): `isolate.New` **resume** (on re-run skip
  repos already `StageCreated`); per-repo **base persisted in `state`** (lets `resolve` populate
  `branch` instead of v0's empty); state **KV + `cas`** (`--expected __ABSENT__`).

## Mutant registry (guard → mutant that must turn it RED)

| guard | mutant |
|-------|--------|
| SHAPE-ENUM-DOUBLE-ENTRY | add/reorder a value in any `All*()` without editing the `want*()` literal copy |
| SHAPE-ENVELOPE-INVARIANTS | add `,omitempty` to `Envelope.Error`, or drop the nil→`[]` coercion for repos/capabilities/warnings/next in `MarshalJSON` |
| SHAPE-SCHEMA | set top-level `additionalProperties:true` (or drop `error` from `required`, or widen a closed enum) in `schema/envelope.schema.json` → `TestSchemaRejectsInvalid` RED |
| SHAPE-FINGERPRINT | rename/retype/reorder any `Envelope` (or nested) field, or edit the schema bytes, without regenerating `contract.lock.json` → `TestContractFrozen` RED |
| INV-NO-LLM | introduce a denylisted LLM/agent-SDK module into `go.mod`/`go.sum` (or empty `llmDenylist`) → `TestNoLLMDependencies` / `TestNoLLMScannerIsNonVacuous` RED |
| META-VACUITY | make `refSubject` ignore the fault (e.g. `if false && Active(refFaultID)`, or always return 42) so the under-fault subprocess passes → `TestMetaVacuity` RED ("harness is vacuous") |
| (fault seam unit) | replace exact `strings.TrimSpace(f) == id` with `strings.Contains` in `activeIn` → the `{"foobar","foo"}` case of `TestActiveIn` RED |
| LAYOUT-PATHS | change any segment literal (`"isolas"`→`"isolate"`, `"repos"`→…) or swap a join order in `layout.go` → `TestPaths` RED vs the hand-written goldens |
| LAYOUT-SAFE | make `validSegment` always-nil → reject cases of `TestSegmentSafety` RED; always-error → the `ok-name_1` accept floor RED |
| OPID-FORMAT | change the time unit (`UnixMilli`→`Unix`), `randLen` (5→4), the `op_` prefix, or drop `strings.ToLower` → `TestNewFormat`/`TestValid` RED |
| CLOCK-DETERMINISM | make `Fake.Rand` return `crypto/rand.Reader` → `TestFakeReproducible` RED; make `NewFake` ignore its seed → `TestFakeSeedSensitive` RED |
| TESTENV-HERMETIC | drop the fixed `GIT_AUTHOR_DATE`/`GIT_COMMITTER_DATE` → seeded SHA ≠ `goldenBaseSHA` → `TestSeedOriginIsDeterministic` RED; drop `GIT_AUTHOR_NAME` injection → ambient username leaks → `TestHermeticIdentity` RED |
| HEAL-ATOMIC-WRITE | replace `WriteFileAtomic`'s temp+rename with an in-place `O_TRUNC` write to the final path (still honoring `FaultBeforeRename`) → under the injected crash the target is torn to the new content → `TestAtomicReplaceIsCrashSafe` RED |
| LAYOUT-BOOTSTRAP | skip the `WiSubdirs` loop in `Bootstrap` → a declared `.wi/` subdir is missing → `TestBootstrapCreatesSubtree` RED; drop `EvalSymlinks` in `Resolve` (`return New(root)`) → a symlinked root keeps its link component → `TestResolveNormalizesSymlinks` RED |
| FLOCK-EXCLUDES | take the lock with `LOCK_SH` instead of `LOCK_EX` in `FileLock.TryLock`/`Lock` → two holders coexist → `TestFlockExcludesSecondHolder` + `TestLockBlocksUntilReleased` RED |
| LOCK-KEYS | change a kind prefix (`repo:`→`repository:`, …) or the `.lock` suffix in `internal/lock/keys.go` → `TestCanonicalKeyStrings` / `TestKeyPathDerivation` RED |
| LOCK-ORDER | make `orderedUnique` leave input order intact (no-op comparator) or skip the dedup → `TestOrderedUniqueIsTotalOrderAndDedups` RED |
| LOCK-MUTEX | treat a refused `TryLock` (`!ok`) as acquired → `TestAcquireRefusesOverlap` RED; or skip `h.Release()` rollback on refusal → `TestAcquireReleasesOnPartialFailure` RED |
| GITEXEC-OFFLINE-BELT | drop `GIT_ALLOW_PROTOCOL=none` from `Run`'s overlay → an offline `ls-remote file://…` succeeds instead of being refused → `TestOfflineRefusesTransport` RED (unit-level half of INV-NO-NETWORK) |
| INV-NO-NETWORK (architecture) | empty `egressAllowed` (or import `os/exec` / reference `GIT_ALLOW_PROTOCOL` in any non-allowlisted package) → `TestNoHiddenNetwork` RED (gitexec+testenv themselves trip, proving the walk + both detectors fire on real source) |
| INV-NO-NETWORK (detector) | make `scanFileForEgress` always return `(false,false)` → `TestNoNetworkScannerIsNonVacuous` RED (a blind scanner would be a silent false negative) |
| GITEXEC-CAPTURE | make `run` swallow a non-zero exit (return `nil` instead of `*ExitError`) → `TestRunSurfacesExitError` RED |
| GIT-FF-ONLY | drop the `merge-base --is-ancestor` precheck in `FastForwardBaseRef` (update-ref unconditionally) → a divergent target advances the base ref → `TestFastForwardRefusesNonFastForward` RED (missing error + moved ref) |
| GIT-CLONE-DETACHED | skip the `switch --detach` in `EnsureClone` (leave `<base>` checked out) → HEAD abbrev-ref is the branch name, not `"HEAD"` → `TestEnsureCloneDetachesAtBaseTip` RED |
| GIT-CLEAN | make `IsClean` ignore `StatusPorcelain` and always return `true` → an untracked file no longer reads as drift → `TestIsCleanReflectsWorkingTree` RED |
| GIT-FETCH | make `Fetch` a no-op (return nil without running `git fetch`) → the remote-tracking ref stays at the old tip → `TestFetchAdvancesRemoteTrackingOnly` RED |
| GIT-DIVERGED | swap the two `rev-list --left-right --count` columns in `DivergedCounts` (read ahead from `fields[1]`, behind from `fields[0]`) → `TestDivergedCountsAheadBehind` RED |
| GIT-WORKTREE | materialize via a standalone `git clone <ssotDir> <path>` instead of `git worktree add --detach` in `AddWorktree` → the result checks out `main` (not detached) and has its own `.git` dir + object store (common-dir ≠ SSOT) → `TestAddWorktreeIsDetachedLinkedAndShared` RED on all three assertions (proves the guard verifies genuine linked-worktree sharing, not just a checkout) |
| GIT-OWNED-REF | flip the namespace `refs/wi/`→`refs/heads/` in `ownedRef` → the marker becomes a stray branch: `refs/wi/owned/` is empty while `refs/heads/` grows a second ref → `TestOwnedRefMarksOwnershipUnderRefsWi` RED on both the "lives under refs/wi at the sha" and "no stray branch" assertions (the round-trip stays GREEN, isolating the decision-#2 namespace property; a no-op `CreateOwnedRef` additionally reddens the absent→present round-trip) |
| MIRROR-FETCH | make `Refresh` skip the `g.Fetch` dial (classify against the stale remote-tracking ref) → behind stays 0, origin_base == local_base, not stale → `TestRefreshFetchesAndClassifies` RED |
| MIRROR-FRESHNESS | hardcode `Stale:false` (or `true`) in `Snapshot.Freshness()`, ignoring the behind count → `TestFreshnessClassifiesStaleByBehindCount` RED (two-sided: a constant fails one branch) |
| MIRROR-PERSIST | make `Store` divert/skip the write (e.g. write `p+".mutant"`) so `Load` can't find it → `TestSnapshotRoundTrips` RED; or drop the `layout.ValidateSegment` call in `metaPath` → `TestStoreRejectsUnsafeRepoName` RED |
| CONFIG-PARSE | make `stripJSONC` a no-op (`return src`) → the golden manifest's comments become JSON syntax errors → `TestParseAcceptsGolden` RED; drop `dec.DisallowUnknownFields()` → the 3 unknown-key cases parse cleanly → `TestParseRejectsInvalid` RED |
| STATE-PERSIST | make `Store` divert the write (`p+".mutant"`) so `Load` can't find it → `TestRecordRoundTrips` RED; or make `UpdateRepoStage` skip its unknown-repo error (`found := true`) so flipping a non-existent repo wrongly succeeds → `TestUpdateRepoStageFlipsOneRepo` RED |
| STATE-DURABLE | replace `lockfs.WriteFileAtomic` with `os.WriteFile` in `Store` (keep `lockfs` referenced so the assertion, not the compiler, reddens) → the injected `WI_FAULT=lockfs.before_rename` no longer aborts so the interrupted flip lands → `TestDurablePartialSuccess` RED |
| ISOLATE-NEW | drop the stop-on-first-fail `return` in `isolate.New` (turn it into `continue`) → the loop materializes the repo AFTER the failed one → `TestNewStopsOnFirstFailWithDurablePartialSuccess` RED on the 3 "db not attempted" assertions (result stage, durable stage, on-disk worktree); or skip the upfront all-pending `state.Store` → the first repo's `UpdateRepoStage` finds no record (`state: no isolate record`) and no durable registry exists to resume from → both `TestNewMaterializesAllReposComplete` + `TestNewStopsOnFirstFail…` RED |
| RESOLVE-BUNDLE | wire per-repo `mirror` to the worktree path (`mirror := worktree`) instead of `layout.Repo` in `resolve.Bundle` → the SSOT mirror equals the worktree, reddening both repos' `Mirror` assertions in `TestBundleProjectsRecordPaths` (proves Bundle distinguishes the `isolas/<task>/<repo>` worktree from the `repos/<repo>` SSOT clone); or `continue` on one repo (drop it from the loop) → the projected `Repos` count/second-repo assertions RED (proves every recorded repo is projected, in order) |
| SHAPE-ONE-ENVELOPE | make `cli.Emit` write the envelope TWICE (a second `w.Write(b)`) → the stream carries two top-level JSON values → `TestEmitWritesExactlyOneEnvelope` RED (second `Decode` returns a document, not `io.EOF`); or drop the trailing `'\n'` (`w.Write` without `append(b,'\n')`) → `TestEmitTerminatesWithSingleNewline` RED |
| SHAPE-FAIL-MATRIX | perturb one pairing in `exitcontract.exitByKind` (e.g. `KindLockHeld`→`ExitRefused`/4 instead of `ExitLocked`/6) → `TestExitCodeForMatchesFailureMatrix` RED on that kind's row vs the independent §3.2 literal copy; or drop a kind whose code collides with the defensive default (e.g. remove `KindInternal`, code 70 == `ExitCodeFor`'s unmapped default) → the value test stays GREEN but `TestExitCodeForIsTotalOverAllKinds` RED (MappedKinds no longer covers `AllErrorKinds`), proving the totality check is non-redundant with the value check |
| SHAPE-ASSEMBLE | in `cli.Success` set `e.OK=false` (break the ok ⟺ error==nil coupling) → `TestSuccessEnvelopeCoupling` RED; or have the shared `spine` omit `Capabilities`/`SchemaVersion` (leave them zero) → `assertCommonFields` reddens in BOTH `TestSuccessEnvelopeCoupling` + `TestFailureEnvelopeCoupling` |
| SHAPE-DRYRUN-EXIT0 | make `cli.ExitFor` return a refusal code when `len(env.Blocked)>0` (treat a would-block verdict as a refusal) → `TestExitForBlockedVerdictsAreExitNeutral` RED (blocked must be exit-neutral); the companion assertion that a genuine usage error on a `--dry-run` still maps to 64 guards against the over-correction (a blanket `if env.DryRun { return ExitOK }` would wrongly swallow it) |
| SHAPE-TEXT-PROJECTION | drop ANY field from `cli.RenderText`/its helpers (e.g. comment out the `worktree` line in `renderRepo`) → that field's unique sentinel leaf is absent from the render → `TestRenderTextIsLossless` RED, naming the exact dropped fact (the independent `collectStringLeaves` reflection walk enumerates every envelope string leaf; the hand-written renderer can't silently omit one). Non-vacuity is inline: ≥25 leaves must be found and a never-present sentinel must NOT match |
| CMD-RESOLVE | in `resolveCmd.Run` drop the `errors.Is(err, state.ErrNoRecord)` branch (`if false && …`) → a missing isolate falls through as a plain error → maps to kind=internal not not_found → `TestResolveMissingIsolateIsNotFound` RED (got `*errors.errorString`, want `*cli.CommandError{not_found}`); or make `newResolveCommand` accept any arg count (skip `len(args)!=1`) → `TestResolveFactoryValidatesArgs` RED |
| CMD-INIT | in `initCmd.Run` open the manifest with `O_TRUNC` instead of `O_EXCL` (clobber-on-reinit) → a re-init silently rewrites the manifest and returns `ActionCreated` (no `fs.ErrExist`→`already_exists`) → `TestInitOnExistingProjectIsAlreadyExists` RED (got a result + nil error, want `*cli.CommandError{already_exists}`, and the manifest is no longer preserved); or make `newInitCommand` accept any arg count (skip `len(args)!=0`) → `TestInitFactoryRejectsArgs` RED |
| CTX-OPID | in `Execute` drop the `ctx = WithOpID(ctx, m.OpID)` injection (`if false {…}`) → the Command observes `""` from `OpIDFrom(ctx)` instead of the minted op_id → `TestExecuteInjectsOpIDIntoContext` RED (saw `""`, want the `Meta.OpID`) |
| DISPATCH-ROUTES | in `cli.resolveCommand` ignore the parsed name and always return a fixed real command (`return "init", positional, true`) → an unknown name wrongly runs a real command (exit 0 not 64) → `TestDispatchRoutesUnknownToUsage` RED, and the 2-token name is mis-stamped with its args dropped → `TestDispatchRoutesTwoTokenCommand` RED; or skip the `op_id` mint (leave `Meta.OpID` empty) → `TestDispatchMintsOpID` RED (`opid.Valid("")` fails on both the success and usage paths) |
| RUN-PIPELINE | in `cli.envelopeFor` drop the durable-partial result-merge (`if false && r != nil { env.Repos = r.Repos … }`) → a partial no longer carries its per-repo detail → `TestExecutePartialCarriesReposAndExitsTwo` RED ("got 0 repos"); or make `Execute` ignore `ExitFor` and `return contract.ExitOK` → every non-zero-exit assertion (CommandError→3, partial→2, internal→70) RED |
| CMD-ISOLATE-NEW | in `isolateNewCmd.Run`, on `res.Status == isolate.StatusPartial` return `(result, nil)` instead of `(result, *CommandError{Kind:partial})` → a partial is mis-reported as a clean success (no error, exit 0) → `TestIsolateNewDurablePartial` RED (`want *cli.CommandError, got <nil>`); or drop the unknown-repo `!ok` not_found branch (skip the `cfg.Lookup` check) → `TestIsolateNewUnknownRepoIsNotFound` RED (no error / wrong kind) |
| SYNC-RUN | drop the `g.FastForwardBaseRef` call in `syncOne` (`if false {…}`, keep the snapshot built from `originSHA`) → after the origin advances the on-disk base ref is never moved → `TestSyncFastForwardsToNewOriginTip` RED (on-disk base frozen at the seed tip, not the new origin tip), while the fresh-materialize + continue-on-fail tests stay GREEN (isolates the mutant to the advance path); secondary: turn the per-repo loop's continue-on-fail into break/return-on-first-error → `TestSyncContinuesOnFailureAndReportsPartial` RED (the reachable repo after the failed one is never synced) |
| CMD-SYNC | in `syncCmd.Run`, on `res.Status == syncpkg.StatusPartial` return `(result, nil)` instead of `(result, *CommandError{Kind:partial})` → a partial sync is mis-reported as a clean success (no error, exit 0) → `TestSyncHandlerDurablePartial` RED (`want *cli.CommandError, got <nil>`), while `TestSyncHandlerSyncsAllDeclaredRepos` stays GREEN (isolates the mutant to the partial-mapping path); alternate: drop the unknown-repo `!ok` not_found branch in `selectRepos` → `TestSyncHandlerUnknownRepoIsNotFound` RED (no error / wrong kind) |

## Decisions taken (from IMPLEMENTATION_PLAN.md §7 open decisions)

- **#G root discovery — RESOLVED 2026-06-30** (not one of the 7 §7 rulings; DESIGN pins no
  root-discovery mechanism, and `wi init` forces it because it DEFINES the root). **Root = the current
  working directory.** `cmd/wi` resolves the layout once at startup via `layout.Resolve(cwd)` and hands
  it to every command through `Deps.Layout`; `wi init` therefore takes NO positional dir operand — it
  scaffolds the workspace at that resolved root (Bootstrap + starter manifest). Rationale: cwd is the
  universal zero-config default; agents invoke `wi` from the workspace root; an explicit override and
  walk-up both add ambiguity better deferred. **Deferred (additive, contract-neutral):** a global
  `--root <dir>`/`-C <dir>` override (lives in `Dispatch.parseGlobals`, applies uniformly to ALL
  commands — the documented overridability mechanism), and parent-directory walk-up (which ancestor is
  the project? — resolve explicitly later, e.g. by the presence of `wi.config.jsonc`). Recorded here +
  in the `cmd_init.go`/`newInitCommand` doc comments; the `cmd/wi` main (a later unit) implements the
  `layout.Resolve(cwd)` startup path.
- **#H malformed-manifest error kind — RESOLVED 2026-06-30** (not a §7 ruling; forced by `isolate new`
  loading the manifest — the closed 11-kind taxonomy has no dedicated "bad config" kind). **A manifest
  that exists but fails `config.Parse` (unknown key, missing url/base, duplicate repo, JSON syntax) maps
  to `kind=usage` (exit 64), NOT `internal`.** Rationale: a malformed manifest is user-fixable INPUT,
  exactly what `usage` (exit 64, "the operator gave bad input") communicates — surfacing it as `internal`
  (exit 70) would wrongly assert a wi BUG and mislead an agent into retrying rather than fixing the file.
  A MISSING manifest (`fs.ErrNotExist`) is distinct → `not_found` + a `wi init` hint (the workspace isn't
  initialized, not malformed). Recorded in the `cmd_isolate_new.go` `Run` doc comment; every later
  manifest-reading handler (`sync`, `repo add`, `isolate rm`) follows the SAME two-way split.
- **#S sync failure semantics — RESOLVED 2026-06-30** (not a §7 ruling; forced by building `internal/sync`
  — the multi-repo `wi sync` needs a defined behavior when one repo fails). **`sync` is CONTINUE-on-fail
  (best-effort per repo), NOT stop-on-first-fail.** Each repo is synced independently under its own
  `repo:<name>` lock; a per-repo failure (unreachable origin, `*git.NonFastForwardError` on a rewound
  origin, a held lock) is recorded in that repo's `RepoOutcome.Err` and the remaining repos are STILL
  attempted; overall `Status=StatusPartial` if any failed (→ exit 2). Rationale: repos are independent
  SSOTs with no inter-dependency, so a network blip or non-ff on one must not strand the others; and each
  repo's sync is atomic + idempotent, so there is nothing to "resume" as a contiguous prefix. This
  deliberately DIFFERS from `isolate.New` (stop-on-first-fail — decision baked into `internal/isolate`),
  because an isolate is ONE coherent multi-repo workspace whose completed set must remain a resumable
  prefix (DESIGN §6.3). `sync.Run`'s Go-error return is therefore reserved for an op-level failure that
  prevents the whole run; in v0 every failure is per-repo, so it returns a nil error and reports via
  Status/Repos. Recorded in the `internal/sync` package doc; the `cmd_sync` handler will project
  `StatusPartial` onto a durable `(result, *CommandError{partial, Action:synced})` exactly as
  `cmd_isolate_new` does (decision #D).
- **#K per-repo `Error.Kind` projection — RESOLVED 2026-06-30** (not a §7 ruling; forced by building
  `cmd_sync`, where a continue-on-fail partial surfaces a per-repo `error` for each failed repo). **Every
  per-repo failure projects to `kind=internal` for now, in BOTH sibling projections
  (`projectRepoOutcome` for isolate new, `projectSyncOutcome` for sync) — typed refinement is DEFERRED to
  the gitexec stderr→kind classifier, not done ad-hoc per handler.** Two of sync's per-repo failures ARE
  cleanly typed today (`*git.NonFastForwardError` → semantically `conflict`, `*lock.HeldError` →
  `lock_held`) and tempting to classify inline. Rejected because: (a) the gitexec stderr→kind classifier
  is the single designated home for per-repo kind derivation and will cover ALL cases (including the
  network/`remote_error` ones the type system can't reach) uniformly — a partial ad-hoc classification now
  would create a two-tier scheme the classifier later has to un-pick; (b) the per-repo `error.kind` is
  INFORMATIONAL detail nested in `repos[]` — it does NOT drive the exit code (the top-level `partial`
  → exit 2 does), so mislabeling it `internal` is a fidelity gap, not a contract violation; (c) it keeps
  the two sibling projections identical, so the classifier fixes both in one move. The deferred-follow-ons
  list tracks "gitexec stderr→kind classifier"; when it lands, both projections route per-repo errors
  through it and this entry is discharged.
- **#F CLI arg-parsing library — RESOLVED 2026-06-30** (an open architectural decision from the PLAN
  stack/Wave-B text, which named `cobra` as a candidate and listed it among the `go.mod` pins; recorded
  as a new resolved item in PLAN §7). **Hand-rolled stdlib parser, NOT cobra** (no new dependency). `internal/cli.Dispatch` does its own parsing: a forgiving single-pass global-flag
  extractor (`parseGlobals` — `--dry-run`, `--format <v>`/`--format=<v>` recognized anywhere, the rest
  positional) + a longest-match command lookup against a `Registry` map (2-token `"isolate new"` beats
  1-token `"isolate"`). Rationale: consistent with the established zero-dep posture (decisions **#6**
  zero new deps, **#C** hand-rolled JSONC) which keeps `INV-NO-LLM` trivially green and the supply-chain
  surface empty; and wi's command surface is small and FIXED (`init`/`repo add`/`sync`/`isolate
  new`/`resolve`/`isolate rm`), so cobra's generation/help/completion machinery would be weight without
  payoff — wi's help + JSON-envelope output are bespoke anyway (the `help-json` capability), not cobra's.
  Agent-friendliness also favors the hand-roll: globals are accepted in ANY position and every malformed
  invocation produces the SAME one-envelope `kind=usage`/exit-64 shape as every other error, rather than
  cobra's free-text stderr. Rejected cobra (and `urfave/cli`, `kong`) for v0; revisit only if the command
  surface grows enough that subcommand/flag wiring becomes a real maintenance cost. Recorded here + PLAN
  §7 (#F struck through) + the `Dispatch` doc comment. Guard `DISPATCH-ROUTES`.

- **#M `mirror_freshness.stale` predicate — RESOLVED 2026-06-30** (not one of the 7 §7 rulings; §7 #1
  only fixed that staleness lives in the structured field, not a warning). `stale = true` **iff
  `behind_origin_as_of_fetch > 0`** — the most current offline-knowable signal, since wi never
  auto-fetches. Rejected a time-based TTL (would need a clock policy or a dial; no TTL exists anywhere
  in the spec). The `stale` bool and the count are non-redundant — the count is `,omitempty` (absent at
  0), so `stale` is the stable field agents branch on. Never-fetched repo → `mirror.ErrNoSnapshot` →
  the `mirror_freshness` block is omitted entirely (≠ "fresh"). Recorded in DESIGN §5.

- **#S `internal/state` stage vocabulary ownership — RESOLVED 2026-06-30** (not one of the 7 §7
  rulings; the spec names the registry but fixes no stage type). The per-repo isolate `Stage`
  (`StagePending` → `StageCreated`) is a small typed-string vocabulary **owned by `internal/state`, NOT a
  closed `internal/contract` enum.** Rationale: the contract owns only the closed *wire* enums, and the
  envelope's `RepoResult.Stage` is already an intentionally free-form `string` projection (confirmed in
  `envelope.go`) — so a closed contract enum would over-constrain a field the contract deliberately left
  open. The v0 isolate lifecycle is `pending → created`; the land-phase vocabulary
  (`pending|landed|blocked`) is a SEPARATE `landstate` concern for v1 and is deliberately not conflated
  with the isolate-materialization stage. If a stage value ever needs to surface as a *closed* envelope
  enum, it moves to `internal/contract` then (per the standing "closed enums live in contract" rule).
  Recorded in the `internal/state` package doc.

- **#C `wi.config.jsonc` parser + manifest schema — RESOLVED 2026-06-30** (not one of the 7 §7 rulings;
  DESIGN names the file `.jsonc` and "repos, defaults, policy" but fixes no field-level schema or parser
  choice). **Parser:** hand-rolled JSONC comment stripper + stdlib `encoding/json` with
  `DisallowUnknownFields`, **zero new deps** — consistent with decision #6 (zero-dep posture) and keeping
  INV-NO-LLM trivially green; a JSONC library was rejected for the read path. **Schema (v0, minimal,
  closed):** top-level `{ defaults?, repos? }`; `defaults` = `{ base }`; each repo = `{ name, url, base? }`
  with effective base = repo `base` else `defaults.base`. Following the SHAPE-SCHEMA precedent (don't
  pre-declare reserved blocks), `policy` and a manifest `version` field are NOT added speculatively —
  they land with their feature at a documented bump. **Deferred:** the AST-preserving *edit* path (for
  `repo add`, DESIGN §line 204) and trailing-comma tolerance are a separate writer unit; this unit is
  read+validate only. Recorded in the `internal/config` package doc.

- **#R `resolve` block field semantics — RESOLVED 2026-06-30** (not one of the 7 §7 rulings; the
  schema + `envelope.go` declare the `resolve` block's fields as plain strings with NO field-level
  intent). `wi resolve <task>` is a **PURE, zero-I/O projection** of a `layout.Layout` + a loaded
  `state.IsolateRecord` — no config dependency, no git, no network, not even a filesystem read (stronger
  than `mirror`'s offline read path). Field mapping: top-level `isolate_root` = `layout.TaskDir`,
  `state_dir` = `layout.StateDir`, `log` = `layout.LogDir` (v0: the dir — no per-task log writer exists
  yet); per repo `worktree` = `layout.Isolate` (the `isolas/<task>/<repo>` linked worktree), `mirror` =
  `layout.Repo` (the `repos/<repo>` SSOT clone), `branch` = **`""`** because v0 isolate worktrees are
  DETACHED (DESIGN §5 — no working branch to report). Every path is sourced from `internal/layout` (the
  sole path owner), never hand-assembled; the CLI owns `state.Load` + mapping `ErrNoRecord` → a
  `not_found` envelope, so `Bundle` stays a total testable function. **Deferred:** (a) populating
  `branch` once a per-repo base is persisted in the state record; (b) drift detection (registry says
  `created` but the worktree is gone on disk) — the contract has no field for it and `doctor`/drift is
  M4, so `Bundle` does not stat paths. Guard `RESOLVE-BUNDLE`. Recorded in the `internal/resolve` package
  doc.

- **#X `remote_error` exit-code mapping — RESOLVED 2026-06-30** (not one of the 7 §7 rulings; DESIGN
  §3.2's exit-code table assigns codes to 10 of the 11 error kinds but leaves `remote_error` without a
  dedicated code). **`remote_error` → exit 70 (`ExitInternal`)**, the catch-all failure bucket it shares
  with `internal`. Rationale: the closed exit-code set is deliberately COARSER than the kind set —
  `dirty_worktree`/`conflict`/`already_exists` already collapse onto 4, and `lock_held`/`mirror_stale`
  onto 6 — so the precise "remote vs internal" distinction lives in the envelope's `kind` field while the
  exit code is the bucket a shell branches on. A remote/transport failure has no slot among the
  refusal (4), lock (6), not-found (3), usage (64), partial (2) or approval (5) codes, so 70 (general
  non-specific failure) is the only consistent home. Rejected minting a new exit code (the set is frozen
  at M0 by `SHAPE-FINGERPRINT`/`contract.lock.json`; a new code would be a contract break, not an
  additive change). `ExitCodeFor` additionally fails-safe to 70 for any *unmapped* kind, so an
  unforeseen future kind degrades to the same general-failure bucket rather than crashing. Recorded in
  the `internal/exitcontract` package doc + guard `SHAPE-FAIL-MATRIX`.

- **#E `--format json` emit output convention — RESOLVED 2026-06-30** (not one of the 7 §7 rulings;
  DESIGN §3.1 pins the envelope SHAPE but not its byte formatting). `cli.Emit` writes **compact,
  single-line** JSON via `contract.Envelope`'s own `json.Marshal` path, then **one trailing newline**.
  Two sub-rulings: (1) **same marshaller as the goldens** — Emit reuses `json.Marshal` (which invokes
  `Envelope.MarshalJSON`) rather than a `json.Encoder` with `SetEscapeHTML(false)` or `SetIndent`, so the
  emitted bytes are byte-identical to the frozen contract goldens + the schema SSOT (a divergent
  serializer would create two inconsistent wire forms of the same envelope and could drift past
  `SHAPE-FINGERPRINT`). Consequence: default Go HTML-escaping (`<`→`<`) is retained — acceptable
  since agents JSON-decode (escaping is transparent) and it keeps one canonical encoding. (2)
  **single-line + trailing newline** — compact (not pretty-printed) so the stream is line-oriented (one
  envelope per line, greppable, log-friendly) and "exactly one envelope" is a decode-then-EOF check; the
  newline is a terminator for line readers, not part of the JSON value. Pretty-printing, if ever wanted
  for human reading, is a `--format text`/pretty concern layered on top, never the machine default.
  Recorded in the `internal/cli` package doc + guard `SHAPE-ONE-ENVELOPE`.

- **#D dry-run exit-0 mechanism + partial-success envelope representation — RESOLVED 2026-06-30** (not
  one of the 7 §7 rulings; DESIGN §3.2 says "every --dry-run → exit 0" and lists `partial`→exit 2 but
  does NOT state the envelope's `ok` value for a partial, whether `error.kind=partial` sits at top level,
  or *how* dry-run exit-0 is achieved). Two coupled rulings, both embodied in `cli.ExitFor` being a
  **pure function of the top-level error** (nil → 0; else `exitcontract.ExitCodeFor(kind)`):
  - **Partial success** = `ok:false` + a **top-level `error.kind=partial`** + per-repo detail in
    `repos[]`, → **exit 2** via the matrix. This is the only representation consistent with `partial`
    being BOTH a closed `error.kind` AND a closed `ExitCode` mapped to each other (`exitcontract`,
    decision none-needed — the table already pairs them) and with §6.3 (durable, resumable). `ok` is
    false because the operation did not fully succeed; the kind field + `repos[]` carry which repos
    completed. `Failure(m, ActionCreated, Error{Kind:KindPartial,…})` is the constructor a partial uses
    (action stays the in-flight verb, the partial verdict rides in `error.kind`).
  - **Dry-run exit-0** is achieved by the **planner discipline**, NOT a special case in `ExitFor`: a
    dry-run that RAN puts its would-block verdicts in `Blocked[]` and leaves `Error` nil, so it falls
    through to exit 0 — `Blocked` is exit-neutral. A blanket `if env.DryRun { return ExitOK }` was
    REJECTED because it would wrongly swallow a genuine top-level error on a `--dry-run` invocation (e.g.
    a usage error that stopped the command *before* any plan was produced must still exit 64). "Every
    --dry-run → exit 0" is thus read as "every dry-run that produced a plan", which the nil-error path
    delivers without `ExitFor` ever consulting `DryRun`. Recorded in the `internal/cli` package doc +
    guards `SHAPE-ASSEMBLE`/`SHAPE-DRYRUN-EXIT0`.

- **#T `--format text` projection scope + formatting — RESOLVED 2026-06-30** (not one of the 7 §7
  rulings; DESIGN §3.1 pins text as a "pure, path-scoped projection of the same struct ... no extra
  facts, no dropped facts" but fixes no layout and does not say *which* fields render). Two coupled
  rulings, embodied in `cli.RenderText(io.Writer, contract.Envelope) error`:
  - **Scope = every field, losslessly.** Text renders EVERY populated field of the assembled envelope —
    including the metadata (op_id/action/schema/capabilities) and every additive block (repos + their
    path/freshness/error, resolve, planned, blocked, warnings, top-level error, next) — formatted as a
    human-readable sectioned report; empty optionals are omitted (absence carries no fact to drop). The
    renderer takes the ALREADY-assembled struct and only reformats it: no new I/O, never a git/state
    re-read, so the json and text wire forms can never disagree (DESIGN §3.1). A "render only the
    operator-significant subset" alternative was REJECTED — "no dropped facts" is literal, and a subset
    renderer would silently lose data an operator piping `--format text` still needs.
  - **Losslessness is proven by an INDEPENDENT derivation.** Because the renderer is hand-written (human
    formatting can't be auto-generated without losing readability), the guard does NOT re-walk the same
    code: `SHAPE-TEXT-PROJECTION` uses a reflection walk (`collectStringLeaves`) that enumerates every
    non-empty string leaf of a maximal envelope by a SEPARATE path, then asserts each appears in the
    render — so a hand-written renderer that forgets a field is caught. A generic *reflective dump* AS
    the renderer was REJECTED: it would make the guard vacuous (renderer and checker would share the one
    walk, so a forgotten field couldn't be detected) and would not be human-readable. Non-vacuity is
    inline (≥25 leaves found + a never-present sentinel that must NOT match). Recorded in the
    `internal/cli` package doc + guard `SHAPE-TEXT-PROJECTION`.

- **#N INV-NO-NETWORK egress allowlist — RESOLVED 2026-06-30** (not one of the 7 §7 rulings; the
  enforcement form of DESIGN §2 #3). The architecture guard permits `os/exec` import + `GIT_ALLOW_PROTOCOL`
  reference only in `{internal/gitexec, internal/testenv}`. **gitexec** is the runtime chokepoint that
  launches every git child and applies the belt; **testenv** is the test-only git-fixture harness — a
  non-`_test.go` support package (so the `_test.go` skip doesn't cover it) that runs git directly via
  `exec.Command`, but is never reachable from `cmd/wi`, so it never ships in a command path. A tree survey
  confirmed those are the only two source files importing `os/exec`, and `GIT_ALLOW_PROTOCOL` appears
  nowhere but gitexec. Scope rule: **go/parser AST scan** (not a token/grep scan) so the belt key inside a
  comment or this guard's own prose can't false-positive; detection is import-of-`os/exec` + belt-key
  string-literal, which is stricter and simpler than tracing `RunNetwork` reachability and needs no
  caller allowlist. Recorded here + in the `nonetwork_test.go` header.

- **#2 Marker-ref mechanism — RESOLVED 2026-06-30** (one of the 7 §7 open decisions). The
  evidence-positive ownership marker reclamation requires (DESIGN §7.1) is a **git ref**
  `refs/wi/owned/<task>/<repo>`, chosen over a git note/reflog AND over a `.wi/index` backref. A ref
  gives **atomic creation** (a single `update-ref`) and **gc-protection** (a ref keeps its commit
  reachable) while living under `refs/wi/*`, NOT `refs/heads/*` — so the marker is never a branch and
  the pristine SSOT (DESIGN §5) never grows a stray branch. The `.wi/index` backref alternative was
  rejected: it would be a second, non-atomic source of ownership truth that could drift from git's own
  ref store and is not gc-aware (git wouldn't protect the referenced objects from a `gc --prune`).
  Implemented as `git.CreateOwnedRef(ctx, ssotDir, task, repo, sha)` (write) + `git.OwnedRefSHA(...)`
  (read, returning `(sha, exists, err)` with a clean absent case), guard `GIT-OWNED-REF`. Recorded here
  + DESIGN §7.1 (already specified the ref) + PLAN §7 #2 (now struck through).

- **#1 `capabilities[]` + warning-code token sets — RESOLVED 2026-06-29.** Capabilities v0 =
  `{help-json, resolve-block, dry-run, partial-success}` (pinned in `Capabilities()`). Warning-code
  v0 = closed `{hydrate_skipped, base_behind_ssot}` (`AllWarningCodes()`), MVP-wired + offline-knowable
  only; staleness stays structured in `mirror_freshness.stale`. Recorded in DESIGN §8 + PLAN §7.

- **#6 Go libs sign-off (lockfs) — RESOLVED 2026-06-30: zero new deps, BOTH halves hand-rolled.**
  The §7 recommendation was "adopt `gofrs/flock` + `google/renameio`"; both legs were overridden to
  zero-dep hand-rolls with concrete rationale (not reflexive NIH).
  - **`WriteFileAtomic`** (not `google/renameio`): the unit's entire fitness is crash-safety, *proven*
    by injecting `WI_FAULT` exactly between the temp write and the rename; a library hides that
    boundary, so the non-vacuity mutant could not be expressed. DESIGN's §M0 file-list already
    specifies the manual recipe and §7 lists hand-rolled as the explicit alternative.
  - **`FileLock`** (not `gofrs/flock`): decided at `flock_unix.go` implementation time (the earlier
    entry deferred this leg pending implementation — not a flip-flop). wi's lock path is unix-only,
    `syscall.Flock(LOCK_EX|LOCK_NB)` is exactly the BSD-flock primitive (pure stdlib ⇒ INV-NO-LLM
    trivially green, no supply-chain surface), and the PID/`boot_id` lock-content + §7.3 auto-break
    self-heal are hand-written regardless, so a library would wrap only one syscall.
  Net: `go.mod` gains no runtime dependency from M0. Owner may override either leg. Recorded in
  DESIGN §6.2 + PLAN §7.

- **#A op_id encoding specifics — RESOLVED 2026-06-29** (DESIGN §3.1 fixed the skeleton
  `op_<base36ts>_<base32rand>` + `.<n>`; these fill the unspecified gaps). Time unit = Unix
  **milliseconds** (rough chronology + human-debuggable, distinct from s/ns). Random = **5 bytes**
  → 8 chars lowercase unpadded standard base32 (`[a-z2-7]`); plenty of within-ms collision
  resistance. Child index **n ≥ 1, no leading zero**; children nest (`.1.2`). op_id is not required
  to be lexicographically sortable (uniqueness comes from the random half). Recorded in DESIGN §8 row
  + `internal/cli/opid` doc comment.

## Conventions

- Module: `github.com/ggkguelensan/workspace-isolation`, Go 1.26.
- Every fitness/guard test names its mutant in the registry above; confirm RED→GREEN per unit.
- Commit one coherent unit at a time, conventional commits, Co-Authored-By trailer. No push / no PR.
