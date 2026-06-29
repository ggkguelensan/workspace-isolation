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

## Next unit (pick this on the next firing)

- **M0/A · Envelope wire type** — `internal/contract/envelope.go`: the `Envelope` struct with
  locked field order + custom `MarshalJSON` enforcing `error:null` (never omitted) and
  `repos:[]` (always an array, never null). Plus nested `RepoResult`, `Error`. Test: marshal
  golden bytes for success/error/partial/dry-run shapes; assert field order + the two
  MarshalJSON invariants. Mutant: drop the `error` field / let `repos` marshal as `null`.
- Then: `schema/envelope.schema.json` (draft 2020-12, additionalProperties:false) as SSOT +
  `SHAPE-SCHEMA` validation guard (needs `santhosh-tekuri/jsonschema` v6); then
  `SHAPE-FINGERPRINT` (sha256 schema↔struct tripwire) + `testdata/contract.lock.json`.

## Mutant registry (guard → mutant that must turn it RED)

| guard | mutant |
|-------|--------|
| SHAPE-ENUM-DOUBLE-ENTRY | add/reorder a value in any `All*()` without editing the `want*()` literal copy |

## Decisions taken (from IMPLEMENTATION_PLAN.md §7 open decisions)

- *(none yet — first open decision needed is the `capabilities[]` + warning-code token set,
  which blocks M0 finalization. Capabilities vocab is already pinned in `enums.go`; warning-code
  vocab still TODO before the Envelope/schema unit locks.)*

## Conventions

- Module: `github.com/ggkguelensan/workspace-isolation`, Go 1.26.
- Every fitness/guard test names its mutant in the registry above; confirm RED→GREEN per unit.
- Commit one coherent unit at a time, conventional commits, Co-Authored-By trailer. No push / no PR.
