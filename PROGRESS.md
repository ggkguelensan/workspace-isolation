# wi — BUILD PROGRESS

Working log for the autonomous build loop. Source of truth for *intent*; the real
build state is always `go build ./... && go test ./...` (trust the build over this file).

Branch: `build/wi` (never commit to `main`). Spec: `DESIGN.md`. Order: `IMPLEMENTATION_PLAN.md`.

---

## Current position

- **Milestone:** M0 (contract spine) — in progress
- **Wave:** A (contract spine, precedes all domain code)

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

## Next unit (pick this on the next firing)

- **M0 · `internal/lockfs` flock half** (`flock_unix.go`) — advisory `flock(2)` to serialize
  concurrent `wi` processes (PLAN §M0 line 166). Per decision #6 adopt `gofrs/flock` (cross-platform
  advisory-lock wrapper); verify it adds no network behavior and no INV-NO-LLM token before
  `go get`. Auto-lock-break is its own later unit (DESIGN §7.3: flock-trustworthy local fs + proven-
  dead PID only) — do NOT bundle it. **Alternatively** take layout `Bootstrap`+EvalSymlinks first
  (now unblocked by `testenv`; pure-stdlib, mkdir the `.wi/` subtree) if preferring zero new deps this
  iteration. Then `internal/lock` (closed lock-key namespace + total-order multi-acquire), then M1
  `gitexec`/`git`/`mirror`.

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

## Decisions taken (from IMPLEMENTATION_PLAN.md §7 open decisions)

- **#1 `capabilities[]` + warning-code token sets — RESOLVED 2026-06-29.** Capabilities v0 =
  `{help-json, resolve-block, dry-run, partial-success}` (pinned in `Capabilities()`). Warning-code
  v0 = closed `{hydrate_skipped, base_behind_ssot}` (`AllWarningCodes()`), MVP-wired + offline-knowable
  only; staleness stays structured in `mirror_freshness.stale`. Recorded in DESIGN §8 + PLAN §7.

- **#6 Go libs sign-off (lockfs) — RESOLVED 2026-06-30, as a SPLIT ruling.** The §7 recommendation
  was "adopt `gofrs/flock` + `google/renameio`." Enacted with a deliberate override on the atomic-write
  half: **`WriteFileAtomic` is hand-rolled (zero new deps)**, not `google/renameio`. Decisive reason —
  this unit's entire fitness is crash-safety, which is *proven* by injecting `WI_FAULT` exactly between
  the temp write and the rename; a library hides that boundary, so the non-vacuity mutant could not be
  expressed. DESIGN's own §M0 file-list note already specifies the manual recipe (`temp + fsync +
  chmod + rename + parent-fsync`), and §7 lists hand-rolled as the explicit alternative — so this is
  enacting the spec, not deviating from it. The **flock** half keeps the recommendation: adopt
  `gofrs/flock` when `flock_unix.go` lands (deferred to that unit; verify no network / no LLM token at
  `go get` time). Owner may override either leg. Recorded in DESIGN §6.2 + PLAN §7.

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
