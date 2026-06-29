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

## Next unit (pick this on the next firing)

- **M0/A · META-VACUITY** — a meta-guard that mechanically asserts the (guard→mutant) registry is
  honest: for each registered pair, applying the mutant must turn its guard RED. Likely realized as a
  build-tag/`WI_FAULT` harness the test toggles, or a documented-and-checked registry table. Closes
  the last methodology guard of Wave A. (`NORM-CORRECT`, the path-scoped golden normalizer, stays
  deferred to Wave B when there is path-bearing CLI output to normalize.)
- Then the first non-contract M0 package: `internal/layout` (all paths: `repos/`, `isolas/<task>/<repo>/`,
  `.wi/` subtree + bootstrap) — needs a unit test pinning the path scheme. After that `internal/lockfs`
  (atomic writes — needs open decision #6), `internal/lock`, `cli/opid`.

## Mutant registry (guard → mutant that must turn it RED)

| guard | mutant |
|-------|--------|
| SHAPE-ENUM-DOUBLE-ENTRY | add/reorder a value in any `All*()` without editing the `want*()` literal copy |
| SHAPE-ENVELOPE-INVARIANTS | add `,omitempty` to `Envelope.Error`, or drop the nil→`[]` coercion for repos/capabilities/warnings/next in `MarshalJSON` |
| SHAPE-SCHEMA | set top-level `additionalProperties:true` (or drop `error` from `required`, or widen a closed enum) in `schema/envelope.schema.json` → `TestSchemaRejectsInvalid` RED |
| SHAPE-FINGERPRINT | rename/retype/reorder any `Envelope` (or nested) field, or edit the schema bytes, without regenerating `contract.lock.json` → `TestContractFrozen` RED |
| INV-NO-LLM | introduce a denylisted LLM/agent-SDK module into `go.mod`/`go.sum` (or empty `llmDenylist`) → `TestNoLLMDependencies` / `TestNoLLMScannerIsNonVacuous` RED |

## Decisions taken (from IMPLEMENTATION_PLAN.md §7 open decisions)

- **#1 `capabilities[]` + warning-code token sets — RESOLVED 2026-06-29.** Capabilities v0 =
  `{help-json, resolve-block, dry-run, partial-success}` (pinned in `Capabilities()`). Warning-code
  v0 = closed `{hydrate_skipped, base_behind_ssot}` (`AllWarningCodes()`), MVP-wired + offline-knowable
  only; staleness stays structured in `mirror_freshness.stale`. Recorded in DESIGN §8 + PLAN §7.

## Conventions

- Module: `github.com/ggkguelensan/workspace-isolation`, Go 1.26.
- Every fitness/guard test names its mutant in the registry above; confirm RED→GREEN per unit.
- Commit one coherent unit at a time, conventional commits, Co-Authored-By trailer. No push / no PR.
