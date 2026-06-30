# wi — IMPLEMENTATION PLAN

Companion to `DESIGN.md`. This is the **build order**: fitness-first (tests before the code they
constrain), self-healing baked in, with non-vacuity proven for every guard. Stack: Go + cobra, git
driven via `os/exec` (not go-git — we need exact worktree/fetch/ref semantics).

---

## 1. Methodology

**Fitness-first.** The frozen contract (`DESIGN.md` §3) and the six hard invariants (§2) are
encoded as executable, CI-gated guards **before** the code they constrain. The published JSON
Schema is the SSOT for envelope shape; the typed Go struct is built to satisfy it and held in
lockstep by a fingerprint tripwire.

**Every guard ships with a non-vacuity proof.** Each fitness function names a paired mutant/fault
(a build-tag mutant or `WI_FAULT=<step>` injection) that MUST turn it red. A `META-VACUITY` guard
walks the (guard → mutant) registry and fails CI if any guard is missing its mutant or if a mutant
fails to flip its guard. A "negative corpus" of hand-crafted bad envelopes is asserted to **fail**
schema validation. This is the defense against false-green tests — the adversary's primary concern.

**Closed sets are double-entry-checked.** The enum lives once in `internal/contract`; the test holds
an independent literal copy. Widening a set requires two reviewed edits + a `schema_version` bump,
gated by `testdata/contract.lock.json` + `TestContractFrozen`.

### 1.1 Tooling (verified current as of 2026)

| concern | library |
|---------|---------|
| JSON Schema validation (draft 2020-12) | `santhosh-tekuri/jsonschema` v6 |
| property-based generation/shrinking | `pgregory.net/rapid` |
| golden / snapshot (normalized envelopes) | `sebdah/goldie` v2 |
| real-CLI integration (txtar fixtures) | `rogpeppe/go-internal/testscript` |
| flock + atomic write | `gofrs/flock` + `google/renameio` (recommended; pending owner sign-off — see §7) |
| stable diff | `google/go-cmp` |
| jsonc parse | `tailscale/hujson` |
| Levenshtein | `agnivade/levenshtein` |

`INV-NO-LLM` bans only LLM SDKs; the above are clean.

---

## 2. Test-first authoring waves

Tests are authored in waves; the code that turns each wave green is built immediately after. Waves
map onto the milestones (§4). Wave names avoid the "v0/v1/v2" release labels to prevent confusion.

### Wave A — contract spine (precedes all code; lands with M0)

Write first: `SHAPE-SCHEMA`, `SHAPE-FINGERPRINT`, `SHAPE-ENUM-DOUBLE-ENTRY`, `NORM-CORRECT`,
`META-VACUITY`, `INV-NO-LLM`.

Then build: `schema/envelope.schema.json` (SSOT) · `internal/contract` (ContractVersion +
SchemaFingerprint consts, typed enums, Allowed* sets, NewError/AddWarning helpers) · good/bad
envelope corpus · path-scoped golden normalizer · mutation/fault-injection harness (build-tag
mutants + `WI_FAULT=<step>`) and the (guard → mutant) registry · `go.mod` with zero LLM SDKs ·
`exitcontract` package routing all exits through `exitcontract.Exit` (no bare `os.Exit` literals).

### Wave B — behavior & invariants (lands across M1–M3)

Write first: `SHAPE-ONE-ENVELOPE`, `SHAPE-FAIL-MATRIX`, `SHAPE-TEXT-PROJECTION`, `INV-DETERMINISM`,
`SHAPE-DRYRUN-EXIT0`, `SHAPE-DRYRUN-ZERO-SIDEEFFECT`, `INV-SSOT-PRISTINE`, `INV-ISOLATION`,
`INV-NO-NETWORK`, `INV-IDEMPOTENT-PROP`, `CONTRACT-PREDICATES`, `INV-PARTIAL-DURABLE`,
`BUILD-CONTRACT-DRIFT`.

Then build: typed Envelope struct + stable-order marshal + single-source text formatter · cobra
tree + `--format` dispatch + central error→envelope→exit mapper · SSOT clone manager (detached +
ff-only update-ref) · isolate materialization via `git worktree add` + `wi`-owned marker ref at add
· desired-vs-observed comparator + idempotency short-circuit (noop vs already_exists per
`CONTRACT-PREDICATES`) · dry-run planner (`planned[]`/`blocked[]`, always exit 0) + fs-fingerprint
snapshot harness · injected network seam + `GIT_ALLOW_PROTOCOL=none` test env · multi-repo isolate
new with per-repo stage accounting · golden CI gate (fail-closed, CI refuses `-update`).

### Wave C — self-heal (lands across M2–M4)

Write first: `HEAL-ATOMIC-WRITE`, `HEAL-CRASH-RECOVER`, `HEAL-LOCK-LIVENESS`,
`HEAL-GC-NO-LIVE-LOSS`, `HEAL-REPAIR-CONVERGE`, `HEAL-LAND-RESUME`, `DOCTOR-NONVACUOUS`,
`HELP-SELF-DESCRIBE`, `AGENT-CAPSTONE`.

Then build: atomic `.wi/` writes + append-safe JSONL journal · durable op journal + offline
roll-forward recovery · `gofrs/flock` locks with `{pid,host,boot_id,op_id}` body + fs-trust
detection + `lock ls/break` · three-way drift reconciler (`isolate repair`, no resurrection) ·
evidence-positive gc (marker refs, `refs/wi/{backup,owned}` protected, unexplained = hard block) ·
`land continue/abort/status` with backup-ref-before-pointer-move · `wi doctor/check` + bounded
`--fix` · JSON help envelope + self-describing `flags[]` + runnable `did_you_mean`/`next` ·
zero-knowledge capstone harness · portability CI matrix.

---

## 3. The fitness suite (56 functions, 5 tracks)

Grouped by what they guard. Each is non-vacuous (paired mutant in the registry).

- **Contract shape (10)** — schema is SSOT + pinned draft 2020-12 + closed enums; one envelope per
  command on success; failure matrix (kind↔exit pairing); text is a path-scoped lossless
  projection; dry-run always exit 0; dry-run zero side effects (hash the whole `<prj>/` tree + git
  refs before/after).
- **Closed-sets drift (13)** — exit/kind/action/capabilities/warning vocab match independent literal
  copy; schema/struct/version fingerprint tripwire; golden + path-scoped normalizer; determinism;
  no-second-ErrorKind architecture test.
- **Invariants (12)** — SSOT pristine (clean, on base, ff-only, no leaked branches); isolation
  (worktree gitdir structural check, no object dup); no-LLM (module-graph denylist); no-network
  (Go seam **and** git-child-process belt via `GIT_ALLOW_PROTOCOL=none`); determinism.
- **Idempotency & partial durability (11)** — noop/already_exists predicate; partial-success
  registry reflects exactly completed repos; crash-injection mid-multi-repo; schema-valid under
  fault; property-based idempotency (run twice → second is noop).
- **Agent usability (10)** — help self-describes; error→help bijection; `next[]` runnable;
  capabilities ⇒ backing command; zero-knowledge capstone (agent drives `wi` from help output
  alone).

### 3.1 Holes the adversary closed (folded into the suite)

- `SHAPE-TEXT-PROJECTION` uses **path-scoped structural** coverage, not a flat token-set (a flat set
  masks per-field drops via aliasing).
- `NORM-CORRECT` proves the golden normalizer is path-scoped and **never over-masks** any
  non-volatile field type (a value-regex normalizer can hide a changed sha-shaped count).
- `INV-NO-NETWORK` adds a **git child-process** egress belt (`GIT_ALLOW_PROTOCOL=none`) on darwin,
  not just the in-process Go dialer seam (which can't see `git fetch` in a child).
- A guard rejects a handler emitting a **raw string** as `error.kind` (bypassing the typed enum) —
  membership/cardinality checks alone don't catch the bypass path.
- A single **cross-track conformance test** pins the two predicates (`DESIGN.md` §3.3) so per-track
  tests can't be green against mutually-inconsistent interpretations.
- `HEAL-GC-NO-LIVE-LOSS` has explicit **negative** tests: gc/repair refuses to delete a worktree
  with reflog-only or committed-but-equal-to-base work; no resurrection of a completed-then-deleted
  isolate; the HEAL-4-reset + HEAL-6-gc **composition** cannot prune a discarded sha.

---

## 4. Milestones

| M | phase | goal | packages / deliverables |
|---|-------|------|-------------------------|
| **M0** | v0 | lock the wire contract, paths, crash-safe primitives | `contract`, `layout`, `lockfs`, `lock`, `cli/opid`; `contract.lock.json` + `TestContractFrozen`; real-git harness; Wave A tests green |
| **M1** | v0 | deterministic git verbs, resolved SSOT posture | `gitexec`, `git`, `mirror`; SSOT clones detached at base tip; sync advances `refs/heads/<base>` via update-ref; `EnsureClone` |
| **M2** | v0 | manifest + runtime state + create/remove/repair isolates | `config`, `state`, `isolate`, `resolve`; registry written per-repo; resolve returns path bundle |
| **M3** | v0 | wire the full MVP through the uniform pipeline | `cli`, `help`, `suggest`; `cmd/wi`; working `init→repo add→sync→isolate new→resolve→isolate rm` end-to-end; CI + `.goreleaser.yaml` + Homebrew tap |
| **M4** | v1 | return work into the mirror base, freshness race closed | `land`, `landstate`, `gc`; `land` + `state cas` + `gc` + `lock` commands; capabilities gain `land`/`land-atomic` |
| **M5** | v2 | phase-3 ergonomics on the frozen contract | `step`, `ports`, `hooks`, `discovery`; capabilities gain `ports`/`hooks`; `needs_approval`/exit 5 now reachable |

MVP = M0–M3. The contract frozen at M0 makes M4/M5 purely additive (minor schema bumps).

---

## 5. First PR — M0 foundation

**Title:** `M0 foundation: contract enums, layout paths, crash-safe lockfs/lock primitives, op_id,
and the real-git test harness`

**Scope:** establish the load-bearing seams every subsystem links against, with zero domain logic.
After this PR the contract, paths, locks, and atomicity rules are unforkable before parallel coding
starts. Only `wi version` is wired (a stub proving the pipeline shape compiles).

**Files:**

- `go.mod` (pin hujson, levenshtein, goldie/v2, ~~cobra~~ [dropped — decision #F, hand-rolled stdlib], x/sys, go-cmp)
- `internal/contract/envelope.go` — Envelope + nested structs, locked field order, `MarshalJSON`
  enforcing `error:null` + always-array `repos`
- `internal/contract/enums.go` — ErrorKind (11), ExitCode, Action, Stage, MainState, LandState,
  Capability; `AllErrorKinds`/`AllExitCodes`; `SchemaVersion="1.0"`; `Capabilities()`
- `internal/contract/contract_test.go` — golden bytes for success/error/partial/dry-run;
  `TestContractFrozen` vs lock file
- `testdata/contract.lock.json`
- `internal/layout/layout.go` (+ `_test.go`) — Layout type; path constructors; Bootstrap;
  EvalSymlinks
- `internal/lockfs/atomic.go` — WriteFileAtomic (temp + fsync + chmod + rename + parent-fsync)
- `internal/lockfs/flock_unix.go` — Acquire/TryAcquire/Release via `x/sys/unix`; network-fs
  detection; LockInfo sidecar
- `internal/lockfs/lockfs_test.go` — crash-point injection; self-heal after killed holder
- `internal/lock/keys.go` — closed Key namespace + `RepoKey`/`IsolateState` helpers + total-order
  `AcquireRepos`
- `internal/cli/opid/opid.go` — frozen `op_<base36ts>_<base32rand>` + `<parent>.<n>` child suffix
- `internal/testenv/` — real-git harness (`t.TempDir` origins seeded with a default branch,
  git-config isolation, EvalSymlinks root, RunWI)
- `internal/golden/` — Normalize + AssertEnvelope/AssertContract

---

## 6. Risk register

| risk | mitigation |
|------|------------|
| parallel coders fork the contract (redefine enums/fields) | `contract` sole owner; `contract.lock.json` drift test; build-time grep test asserts no second ErrorKind |
| SSOT posture regresses (someone reintroduces checkout+merge) | `git.FastForwardBaseRef` is the only base-mutation path; integration test asserts SSOT HEAD stays detached |
| freshness race reopens (sync/land use different keys) | `lock.Key` closed namespace; both call `lock.RepoKey(name)`; concurrency test runs sync+land simultaneously, asserts serialization |
| partial-success unsound (registry written once at end) | `state.UpdateRepoStage` after each worktree add; crash-injection test kills mid-multi-repo, asserts registry = completed set |
| **hydrate leaks a secret** | default-deny; require gitignored AND allowlisted AND not in hard-deny (`.env`/`*.pem`/`id_*`/`*.key`); reject `*`/`**` globs at parse; `--dry-run` shows every planned copy |
| golden snapshots flap on volatile fields | `golden.Normalize` placeholders for paths/sha/op_id/ts; FakeClock; repos sorted; marshal via structs |
| **gc/land destroys live work** (adversary's top finding) | evidence-positive reclamation (§7 DESIGN); `refs/wi/{owned,backup}` protected; unexplained orphan = hard block; no `reset --hard` in land; `doctor --fix` re-detects between heals |
| auto-lock-break corrupts on network fs | detect `.wi/` fs type; refuse auto-break unless flock-trustworthy local fs + proven-dead PID |
| goreleaser `brews` deprecated | pin `~> v2`; never auto-upgrade major; cask rejected |
| Apple Git lags upstream | CI matrix: macos-latest (Apple Git) + pinned-minimum git floor (2.38 for `merge-tree --write-tree`); `git --version` capability probe at startup |

---

## 7. Open decisions (need owner ruling before the relevant milestone)

1. ~~**`capabilities[]` + `AllowedWarningCodes` initial token sets**~~ — **RESOLVED 2026-06-29.**
   `capabilities[]` v0 = `{help-json, resolve-block, dry-run, partial-success}` (pinned in `enums.go`
   `Capabilities()`). Warning-code v0 = closed `{hydrate_skipped, base_behind_ssot}` (`AllWarningCodes()`),
   limited to MVP-wired, offline-knowable codes; staleness stays in structured `mirror_freshness.stale`,
   not a warning. Both double-entry-guarded; grow only with a schema bump.
2. ~~**Marker-ref mechanism**~~ — **RESOLVED 2026-06-30.** `refs/wi/owned/<task>/<repo>` (git ref)
   chosen over note/reflog AND over a `.wi/index` backref: a ref gives atomic creation (one
   `update-ref`) and gc-protection (the ref keeps its commit reachable) while living under `refs/wi/*`,
   NOT `refs/heads/*`, so it never appears as a stray branch in the pristine SSOT (DESIGN §5). A
   `.wi/index` backref was rejected — it would be a second, non-atomic source of truth that could
   drift from git's own ref store and is not gc-aware. Implemented as `git.CreateOwnedRef` +
   `git.OwnedRefSHA` (guard `GIT-OWNED-REF`). *Blocked: M2 (marker at add) — now unblocked.*
3. **`boot_id` on darwin** — derive from `sysctl kern.boottime` (no `/proc`); confirm stability
   across sleep/wake and PID-reuse-guard soundness. *Blocks: M4 (lock liveness).*
   **RESOLVED 2026-06-30:** `internal/host.BootID()` — darwin via the `sysctl(2)` SYSCALL
   (`syscall.Sysctl("kern.boottime")`, decode little-endian `tv_sec`), NOT a subprocess (importing
   `os/exec` would trip INV-NO-NETWORK); linux via `/proc/sys/kernel/random/boot_id`. Boot-stable,
   unchanged by sleep/wake, differs across reboots → the {boot_id, pid} pair is reuse-safe. Guard
   `HOST-BOOTID`. Supported platforms = linux + darwin (other unix deferred to M5's portability matrix).
4. **isolate-remove recovery policy** — roll-forward (finish deletion) vs roll-back (restore);
   leaning roll-forward, but then an interrupted remove can't be undone by re-running. *Blocks: M2.*
   **RESOLVED 2026-06-30: roll-FORWARD, decided per-op by the durable op journal's furthest-reached
   phase (HEAL-4, `journal.Classify`).** An op that reached `committed` (crossed its point of no
   return) is FINISHED on the next offline startup — an interrupted isolate-rm completes its deletion,
   it is never restored (accepting that an interrupted remove cannot be undone by re-running, exactly
   the documented trade-off). An op that only reached `intent` (crashed before commit) is ABANDONED:
   recovery neither finishes it (nothing durably began) nor undoes it (there is no roll-back), leaving
   any partial artifacts to the evidence-positive heals (isolate repair / gc). Guard `HEAL-CRASH-RECOVER`.
5. **SIGINT / exit-130 coverage** — add an explicit per-long-running-command SIGINT row (clean
   partial-state flush + exit 130 + well-formed envelope) or accept the SIGKILL-sweep folding.
6. **Go libs sign-off** — **RESOLVED 2026-06-30: zero new deps, both halves hand-rolled.**
   `WriteFileAtomic` hand-rolled (keeps the `WI_FAULT` crash-window seam in-tree for HEAL-ATOMIC-WRITE
   non-vacuity); `FileLock` hand-rolled on `syscall.Flock` (`flock_unix.go`, unix-only) rather than
   `gofrs/flock` — pure stdlib, INV-NO-LLM trivially green, and the PID/self-heal layer is hand-written
   anyway. See DESIGN §6.2 + PROGRESS #6.
7. **Supported git version window** — floor/ceiling, include next-rc cell? Determines the
   portability matrix and the doctor git-version-floor warning predicate.
8. **CLI arg-parsing library (cobra vs hand-rolled stdlib)** — **RESOLVED 2026-06-30: hand-rolled
   stdlib, NOT cobra** (decision #F). `internal/cli.Dispatch` parses argv itself — a forgiving
   single-pass global-flag extractor (`--dry-run`, `--format <v>`/`--format=<v>`, recognized anywhere) +
   a longest-match command lookup against a `Registry` map (2-token `"isolate new"` beats 1-token
   `"isolate"`). Consistent with the zero-dep posture (#6, #C → INV-NO-LLM stays trivially green, no
   supply-chain surface) and wi's small FIXED command surface; cobra's generation/help/completion
   machinery would be weight without payoff since wi's help + JSON-envelope output are bespoke
   (`help-json` capability), and a hand-roll lets every malformed invocation produce the SAME
   one-envelope `kind=usage`/exit-64 shape (agent-friendly) rather than cobra's free-text stderr.
   Guard `DISPATCH-ROUTES`. **Consequence:** the speculative `cobra` pin below is dropped — `go.mod`
   gains no arg-parsing dependency. See PROGRESS #F. *Blocked: M3 (CLI surface) — now unblocked.*
