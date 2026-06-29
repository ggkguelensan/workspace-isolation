# wi — BUILD PROGRESS

Working log for the autonomous build loop. Source of truth for *intent*; the real
build state is always `go build ./... && go test ./...` (trust the build over this file).

Branch: `build/wi` (never commit to `main`). Spec: `DESIGN.md`. Order: `IMPLEMENTATION_PLAN.md`.

---

## Current position

- **Milestone:** M1 in progress (git verbs / SSOT posture). M0 complete; `gitexec` runner+belt and
  `git.FastForwardBaseRef` (the SSOT keystone) landed.
- **Wave:** A complete (modulo `NORM-CORRECT`, deferred to Wave B); into Wave B domain code

## Done

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
  error). Reverted → GREEN. Remaining M1 verbs (`EnsureClone`, dirty/status checks) follow as their own
  units; then `internal/mirror`.

## Next unit (pick this on the next firing)

- **M1 · `internal/git` · `EnsureClone` + working-tree status verbs.** Add the two verbs the SSOT
  lifecycle needs next: (1) `EnsureClone(ctx, dir, originURL, base)` — lazily create an absent SSOT
  clone **detached at the base tip** (DESIGN §5 line 203: `wi sync --repo r` clones on first use;
  `repo add` stays a pure manifest edit), idempotent if `dir` already a valid clone. Cloning is the
  ONE place network is allowed → must use `gitexec.RunNetwork` (everything else stays offline). Guard:
  a fresh clone lands on a **detached HEAD** at `refs/heads/<base>`'s tip (assert `rev-parse
  --symbolic-full-name HEAD` is detached / `HEAD` == base tip but no branch checked out); second call
  is a noop (no re-clone). Mutant = clone without detaching (leave base checked out) → detached-HEAD
  assertion RED. (2) A dirty/clean predicate (`IsClean`/`StatusPorcelain`) for the SSOT-pristine
  invariant. Use `testenv.SeedOrigin` as the clone source (local `file://`, but clone is the network
  verb so route via `RunNetwork`). After these, `internal/mirror` (cached freshness, never dials on
  read paths).

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
| GITEXEC-CAPTURE | make `run` swallow a non-zero exit (return `nil` instead of `*ExitError`) → `TestRunSurfacesExitError` RED |
| GIT-FF-ONLY | drop the `merge-base --is-ancestor` precheck in `FastForwardBaseRef` (update-ref unconditionally) → a divergent target advances the base ref → `TestFastForwardRefusesNonFastForward` RED (missing error + moved ref) |

## Decisions taken (from IMPLEMENTATION_PLAN.md §7 open decisions)

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
