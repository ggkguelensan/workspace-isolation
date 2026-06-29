# wi — DESIGN

`wi` (workspace-isolation) is a Go CLI for **multi-repo workspace isolation**, built to drive
parallel AI coding agents (Claude Code / pi inside cmux). It is a **deterministic primitive**: no
LLM inside, no interactive TUI, no shell takeover. The agent is the brain; `wi` provides a
machine-readable shape and contract that a CLI agent can drive blind.

This document is the **locked specification**. Parallel implementation links against it. Anything
not written here is not yet decided — see `IMPLEMENTATION_PLAN.md` § Open Decisions.

---

## 1. The model

Within a project directory `<prj>/`:

```
<prj>/
  wi.config.jsonc        committed declarative manifest (repos, defaults, policy)
  repos/<repo>/          SSOT: reference clones. NEVER mutated beyond ff-advancing the base ref.
  isolas/<task>/<repo>/  isolates: git worktrees derived from the SSOT. One isolate per task.
  .wi/                   machine runtime state (gitignored). Never hand-edited.
    locks/  state/  log/  mirrors/  land/  ports/  trust/
```

**Flow:** user tells the agent "start task X" → agent decides which repos are needed → `wi isolate
new X --repo a --repo b` creates an isolate bundling worktrees of those repos → agent works inside
`isolas/X/` → `wi land X` returns the work into the SSOT base.

Isolation is **git worktrees from a normal (non-bare) SSOT clone** — native object-store sharing,
no copies. (`isolation: "alternates"` is a reserved future mechanism, not built.)

---

## 2. The six hard invariants

These are non-negotiable and each is guarded by a dedicated, non-vacuous fitness function
(`IMPLEMENTATION_PLAN.md` § Fitness Suite).

1. **SSOT is never mutated** beyond `git fetch` + fast-forward of its base ref. No work, no dirt,
   no stray feature branches in `repos/<repo>`.
2. **Isolation**: an isolate shares the SSOT object store via worktree (no object duplication); a
   dirty isolate can never dirty the SSOT.
3. **No LLM, no hidden network**: the module graph is disjoint from any LLM SDK; offline commands
   (and startup recovery) perform **zero** network dials — including git child processes.
4. **Determinism**: a command's normalized envelope is byte-identical across runs (modulo `op_id`
   and timestamps).
5. **One envelope**: every command — success and every failure — emits exactly one well-formed
   envelope on stdout and nothing else; JSON is the default.
6. **Durable partial success**: a multi-repo op that fails partway leaves durable, resumable state
   (exit 2), never a silently half-written registry.

---

## 3. The wire contract

### 3.1 The envelope

The **published JSON Schema** (`schema/envelope.schema.json`, draft 2020-12,
`additionalProperties:false`, closed enums) is the **single source of truth** for shape. The typed
Go `Envelope` struct is built to satisfy it and kept in lockstep by a fingerprint tripwire (§5.4 of
the plan). JSON by default; `--format text` is a **pure, path-scoped projection** of the same
struct (no extra facts, no dropped facts) — the renderer takes the same struct as the JSON
marshaller and never re-reads git.

Top-level required keys (locked field order):

```jsonc
{
  "schema_version": "1.0",
  "capabilities": ["help-json", "resolve-block", "dry-run", "partial-success"],
  "op_id": "op_<base36ts>_<base32rand>",   // child jobs: "<parent>.<n>"
  "command": "isolate new",
  "ok": true,
  "action": "created",                      // closed enum, see 3.2
  "dry_run": false,
  "repos": [ /* always an array, even when empty */ ],
  "warnings": [ /* {code, message, repo?} — code from closed vocab */ ],
  "next": [ /* suggested follow-up commands, runnable verbatim */ ],
  "error": null                              // null on success; never omitted
}
```

Additive blocks (all `omitempty`, reserved in v0 so v1/v2 are minor bumps):
`resolve` (path bundle), `land_state`, `mirror_freshness`, `planned`/`blocked` (dry-run),
`tethers`, `ports`, `hooks`.

`error` when present:

```jsonc
{ "kind": "dirty_worktree", "code": "ssot_dirty", "message": "...",
  "repo": "api", "help": "wi help isolate", "did_you_mean": ["isolate new"] }
```

**Agents branch on `kind` and the exit code, never on `message` text.** `message` is the only
free-text field and is never asserted on in tests.

### 3.2 Closed sets (the contract spine)

All live **once** in `internal/contract` and are double-entry-checked against an independent
literal copy in the test (widening a set requires two reviewed edits). `gitexec`, `help`, `land`,
`testing` **import** them — they never redefine them.

**`action`** (6): `created` · `removed` · `synced` · `landed` · `read` · `noop`

**`error.kind`** (11): `usage` · `not_found` · `dirty_worktree` · `conflict` · `lock_held` ·
`mirror_stale` · `needs_approval` · `already_exists` · `partial` · `remote_error` · `internal`

**Exit codes** (closed):

| code | meaning |
|------|---------|
| 0 | success (**including `noop`**, and **every `--dry-run`**) |
| 2 | partial success (durable, resumable) |
| 3 | not_found |
| 4 | refused at exec: `dirty_worktree` / `conflict` / `already_exists` |
| 5 | needs_approval (only reachable once hooks ship — v2) |
| 6 | `lock_held`, or `mirror_stale` on the `land` path |
| 64 | usage error |
| 70 | internal error |
| 130 | interrupted (SIGINT) |

`wi schema` annotates each exit code / kind with `reachable: true|false` for the current build, so
the agent never writes a branch for a verdict no wired command can produce.

**`capabilities[]`** advertises **only wired commands**. Closed vocabulary:
`help-json` · `resolve-block` · `dry-run` · `partial-success` · `land` · `land-atomic` · `state-kv`
· `ports` · `hooks` · `remote-discovery`. v0 lights the first four; `land*` at M4; `ports`/`hooks`
at M5.

### 3.3 Two predicates pinned once (cross-track conformance test enforces them)

The adversary review found these decided **inconsistently** across design tracks. Locked here:

- **`noop` vs `already_exists`**: same-name + **congruent, fully-materialized** spec → `action:noop`
  / exit 0 (idempotent repeat). Same-name + **divergent** spec (different url, or a repo's branch on
  a different sha) → `error.kind:already_exists` / exit 4.
- **`mirror_stale` / exit 6**: offline commands **never auto-fetch and never exit 6 for staleness** —
  they **warn** (`mirror_freshness.stale=true`). Exit 6 is reserved for `lock_held` and for the
  explicit `land` path when the isolate base is behind the SSOT base. **`wi` never auto-rebases.**

---

## 4. Package architecture

Strict one-directional dependency stack; `internal/contract` at the bottom, `internal/cli` at the
top. Domain packages return plain typed structs and supply candidate-string slices upward — they
**never** assemble envelopes or compute exit codes.

```
cmd/wi/main.go            the single os.Exit; wires cobra (SilenceErrors/SilenceUsage)

internal/cli              uniform pipeline: Command iface → typed *Result → Runner maps
  internal/cli/opid       Result→ExitCode via compiled table, serializes Envelope, threads
                          op_id (generated once in PersistentPreRunE), renders json|text,
                          coerces dry-run→exit 0. Single caller that injects help/did_you_mean/next.

internal/help             progressive-disclosure help model + next[] rules (SOLE owner)
internal/suggest          the ONE Levenshtein engine + candidate producers (SOLE owner)

internal/land  (v1)       rebase-onto-mirror + freshness-guarded update-ref + durable parked stages
internal/landstate (v1)   .wi/land/<task>.json per-repo phase accounting
internal/gc    (v1)       evidence-positive reclamation

internal/isolate          N-repo worktree lifecycle; writes each RepoRecord incrementally
internal/resolve          derives the path bundle for `wi resolve` (was ownerless — now owned)
internal/config           parse/validate/AST-preserving edit of wi.config.jsonc (owns ONLY the file)
internal/state            SOLE owner of registry + namespaced KV + cas
internal/mirror           cached freshness (never touches network on read paths)

internal/git              deterministic typed git verbs (on gitexec)
internal/gitexec          git via os/exec; hermetic env; stderr→kind regexp table

internal/lock             closed lock-key namespace + total-order multi-acquire (SOLE owner)
internal/lockfs           the ONLY syscalls: WriteFileAtomic + flock w/ self-heal (SOLE atomic writer)
internal/layout           ALL path construction + .wi/ subtree bootstrap; EvalSymlinks-normalized
internal/contract         SOLE owner of closed enums + Envelope wire type + SchemaVersion
internal/clock            injectable time / op_id randomness
```

`CommandMeta` is folded into the `help` registry (one metadata source, validated against the live
`pflag.FlagSet`) so help can never lie about a command's flags, exit codes, or kinds.

---

## 5. The SSOT posture — the keystone decision

**Validated empirically on git 2.50.1.** The SSOT clone is kept on a **detached HEAD at the base
tip**. Both `sync` (v0) and `land` (v1) advance the base ref identically:

```
git merge-base --is-ancestor <current> <new>   # fast-forward safety check
git update-ref refs/heads/<base> <new>          # no checkout, no merge
```

**Why not `checkout <base>` + `merge --ff-only`?** Because git **refuses** to fetch/merge into a
branch that is checked out in a worktree. `land` must move `refs/heads/<base>` while the SSOT clone
itself holds no checked-out branch — a detached HEAD is exactly that. This single ruling lets v0
`sync` and v1 `land` coexist on one clone with **zero v1 rework**, and `git.FastForwardBaseRef` is
the **only** base-ref-mutation path in the entire codebase (an integration test asserts the SSOT
HEAD stays detached after an op battery).

`git.EnsureClone` lazily clones an absent SSOT on first `wi sync --repo r` (detached at base tip);
`repo add` stays a pure manifest edit. `main_state` is classified against the **local** mirror base
`refs/heads/<base>`; behind-origin is surfaced separately as the **cached**
`mirror_freshness.behind_origin_as_of_fetch` (read paths never dial the network).

---

## 6. Concurrency & durability

### 6.1 Locking

`internal/lock` publishes a **closed key namespace** and a total acquisition order. All mutators use
the exact keys:

- `repo:<name>` — base-ref mutation. **`sync` and `land` acquire the identical key**, closing the
  freshness race by linearization (the `targetMirrorSHA` recheck stays as belt-and-suspenders).
- `project-registry` — registry writes.
- `isolate-state:<task>` — per-isolate state.

`flock(2)` `TryLock` is authoritative: `EWOULDBLOCK` → exit 6 `lock_held`. Auto-break of a stale
lock happens **only** on a flock-trustworthy local filesystem with a proven-dead holder (§7.3).

### 6.2 `.wi/` writes

`internal/lockfs.WriteFileAtomic` is the **single** atomic writer reused by every `.wi/` writer
(mirror, registry, state, land, ports, trust): write to a temp file in the same dir → `fsync` →
`chmod` to the caller's mode → `rename` (atomic on one fs) → `fsync` the parent dir. Eliminates the
three-duplicate-writers smell.

> **Decision #6 (Go libs, lockfs) — split ruling.** `WriteFileAtomic` is **hand-rolled** (no
> `google/renameio`): owning the temp→rename boundary in-tree is what lets the `WI_FAULT`
> fault-injection seam abort *exactly* in the crash window, which is how guard `HEAL-ATOMIC-WRITE`
> proves crash-safety is non-vacuous — a library hides that seam. The advisory **flock** half adopts
> `gofrs/flock` when `flock_unix.go` lands.

### 6.3 Partial-success durability

`isolate new` is **stop-on-first-fail with durable, not-rolled-back** completed repos (exit 2,
resumable). `state.UpdateRepoStage` is called **after each worktree add**, interleaved per-repo, so
a crash mid-multi-repo leaves a registry that reflects exactly the completed repos. `op_id` is
stored on the `IsolateRecord`/`RepoRecord`. (`--atomic` validate-all-then-apply is reserved for
`land` in v1.)

---

## 7. Self-healing architecture

The adversary's dominant finding: **the danger is not failing to heal — it is healing in a way that
destroys live, unpushed work.** Three hard rules close every data-loss path found.

### 7.1 Reclamation is evidence-POSITIVE

A worktree/branch is reclaimable **only** if a `wi`-owned marker ref `refs/wi/owned/<task>/<repo>`
proves `wi` created it, **AND** it is clean, **AND** not ahead of base, **AND** not journaled as
live. An **unexplained orphan** (no marker, or live-looking with lost state) is a **HARD BLOCK** —
never auto-pruned, surfaced loudly as `orphan_unexplained`. `refs/wi/owned/*` and `refs/wi/backup/*`
are **protected from gc**. gc never uses `--prune`-style reclaim on unexplained objects.

### 7.2 No `git reset --hard` in land rollback

`land` writes a backup ref **before** any pointer move. Recovery is `land continue` / `land abort` /
`land status` over the durable `.wi/land/<task>.json` (per-repo phase `pending|landed|blocked`,
`backup_sha`). Offline push reconciliation **never flips a phase to `landed` on an offline run** —
it leaves `phase=blocked` and re-observes on the next online invocation. No blind re-push.

### 7.3 Auto-lock-break only on a trustworthy fs

`wi` detects the `.wi/` filesystem type and **refuses** all auto-break where flock is not known
trustworthy (network fs). PID age alone never authorizes a break — only a proven-dead PID
(`boot_id` mismatch or `Kill(pid,0)==ESRCH`) on a flock-trustworthy local fs.

### 7.4 The heal mechanisms

| id | mechanism |
|----|-----------|
| HEAL-1 | three-way isolate drift reconciler (`isolate repair`) — classify each repo cell; re-materialize MISSING_WORKTREE **only** if not a completed-then-deleted op (no resurrection) |
| HEAL-2 | evidence-positive gc (§7.1) |
| HEAL-3 | stale-lock break with flock-trustworthy-fs gate (§7.3) |
| HEAL-4 | durable op journal (`intent→committed→done`) + **offline-only** roll-forward recovery on startup |
| HEAL-5 | parked-land resume/abort with backup-ref safety (§7.2) |
| HEAL-6 | mirror-stale **refusal** at land (exit 6), rebase-or-refuse, never auto-rebase |
| HEAL-7 | atomic `.wi/` writes (§6.2) |
| HEAL-8 | `wi doctor` read-only diagnosis + bounded `--fix` (§7.5) |

### 7.5 `wi doctor` (alias `wi check`)

Read-only health diagnosis emitting the frozen envelope. Eight detectors → each finding maps to a
frozen `error.kind` + a stable sub-code: SSOT cleanliness (`ssot_dirty`/`ssot_stray_branch`),
three-way isolate drift, orphan inventory (`orphan_unexplained` is loud), lock inventory + liveness
(`fs_unsafe_for_locks`), pending journal/parked ops, `.wi` state parseability, mirror staleness
(WARNING only — never refreshes, never exit 6 here), environment probes (git version floor, fs
type). Exit = the worst finding's code. **Never touches the network.**

`--fix` dispatches each fixable finding to its owning **safe-tier** heal and **re-runs full
detection between every heal**, stopping and reporting on any lock break — so composed safe heals
can never cascade into a data-loss chain in one invocation.

---

## 8. Resolved decisions (locked)

| decision | ruling |
|----------|--------|
| SSOT HEAD posture | detached HEAD + `update-ref` for both sync and land (§5) |
| per-repo lock | sync & land acquire identical `repo:<name>` key in v0 (§6.1) |
| closed-enum ownership | `internal/contract` sole owner; all others import; drift-guarded by `contract.lock.json` |
| warning-code vocab (v0) | closed set `{hydrate_skipped, base_behind_ssot}` (decision #1); only MVP-wired, offline-knowable codes; staleness stays in structured `mirror_freshness.stale`, not a warning; grows only with a schema bump |
| `state cas` ownership | `internal/state` sole owner; land consumes; `--expected __ABSENT__` sentinel frozen |
| help/did_you_mean/next ownership | `suggest` owns Levenshtein, `help` owns model, cli envelope writer is sole injector; defer unknown-command typos to cobra's `SuggestionsFor` |
| path / `.wi/` ownership | new `internal/layout` owns all paths + bootstrap |
| initial clone | `git.EnsureClone` lazy on first `wi sync --repo r` |
| `main_state` reference | local mirror base `refs/heads/<base>`; behind-origin = cached field |
| reserved envelope fields | all `omitempty` in v0 (M0 struct + golden tests); capabilities gated to wired commands |
| op_id format | `op_<base36ts>_<base32rand>`; child suffix `<parent>.<n>`; cli Runner logs one JSONL summary per op (git ops take no logger). **v0 specifics (PROGRESS decision #A):** ts = Unix **ms** base36; rand = **5 bytes** → 8-char lowercase unpadded base32 `[a-z2-7]`; child `n ≥ 1` no leading zero, nests; not lexicographically sortable. |
| isolate-new atomicity | stop-on-first-fail, durable-partial, exit 2, resumable |
| land atomicity (`--atomic`) | validate-all-then-apply (`merge-tree` pre-check) in v1; post-apply surprise degrades to exit 2 |
