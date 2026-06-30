# wi — workspace isolation

**Deterministic, multi-repo workspace isolation for parallel AI coding agents.**

`wi` lets many agents (or many humans) work on the *same set of repositories* at the
same time without stepping on each other. Each task gets its own set of isolated git
worktrees carved from a pristine local mirror; when the work is done, `wi land`
fast-forwards it back onto the base branch. Every command speaks one stable JSON
envelope and a fixed exit-code contract, so an agent can drive `wi` without ever
parsing prose.

It is a small, sharp **primitive**, not a framework: no LLM dependency, no hidden
network, no daemon. One static Go binary, one runtime dependency (a JSON-schema
validator). The agent is the brain; `wi` is the deterministic hands.

---

## Why

Running several agents against one repo means merge chaos, half-applied changes, and
"who moved my branch?". The usual fixes — one clone per agent, or `git worktree` by
hand — drift out of sync and leave orphaned debris nobody can safely clean up.

`wi` makes isolation a first-class, **crash-safe, self-healing** operation:

- **Single Source Of Truth (SSOT) mirror** — one local reference clone per repo, kept
  detached at the base tip. Isolates are cut from it; the SSOT itself is never dirtied
  and never grows stray branches.
- **Isolated worktrees** — `wi isolate new <task>` materializes one worktree per repo
  under `isolas/<task>/<repo>/`. The agent works there, fully isolated.
- **Fast-forward-only landing** — `wi land` advances the base ref *only* by
  fast-forward; it never force-pushes, never `reset --hard`s, and parks a durable,
  resumable record if a repo can't land cleanly.
- **Evidence-positive reclamation** — `wi gc` only deletes what it can *prove* it owns
  (via `refs/wi/owned/*` marker refs). An unexplained orphan is a loud refusal, never a
  silent delete.
- **Self-healing** — durable op-journal + offline roll-forward recovery, liveness-aware
  locks (`pid`/`host`/`boot_id`), and three-way drift repair mean a crashed agent leaves
  a workspace `wi` can put back together.

---

## Install

`wi` is a single Go binary (Go 1.26+). Install it onto your `PATH` with:

```bash
git clone https://github.com/ggkguelensan/workspace-isolation.git
cd workspace-isolation
go install ./cmd/wi          # → $(go env GOPATH)/bin/wi   (usually ~/go/bin/wi)
```

If `~/go/bin` isn't on your `PATH` yet:

```bash
echo 'export PATH="$(go env GOPATH)/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc
```

Verify:

```bash
wi help        # prints the command catalog as a JSON envelope
```

> Release binaries (`.goreleaser.yaml`) and a Homebrew tap are scaffolded for tagged
> releases; until then, `go install` is the supported path.

---

## Quick start (the happy path)

```bash
# 1. Bootstrap a workspace in the current directory (creates .wi/ + wi.config.jsonc)
wi init

# 2. Declare the repos you want to work across
wi repo add api  https://github.com/you/api.git  --base main
wi repo add web  https://github.com/you/web.git  --base main

# 3. Fetch them into the local SSOT mirror
wi sync

# 4. Cut an isolated worktree set for a task (one or more repos)
wi isolate new add-checkout api web

# 5. Find out where to work, then work there
wi resolve add-checkout        # → path bundle: isolas/add-checkout/api, .../web
#    ... edit, commit inside those worktrees as usual ...

# 6. Land the work back onto each repo's base branch (fast-forward only)
wi land add-checkout api web

# 7. Tear down the isolate when you're done
wi isolate rm add-checkout
```

If a land can't fast-forward cleanly it **parks** instead of failing destructively:

```bash
wi land status   add-checkout   # per-repo phase: landed / blocked / pending
# ... rebase the blocked worktree onto the new base, then ...
wi land continue add-checkout   # re-attempt the blocked repos
# ... or, to undo everything that did land and rewind to the pre-land state:
wi land abort    add-checkout
```

Add `--dry-run` to any mutating command to see the plan with zero side effects (always
exits `0`). Add `--format text` for a human-readable projection of the same envelope.

---

## How it works

```
<root>/
  wi.config.jsonc          # committed manifest: which repos, which base branch
  repos/<repo>/            # SSOT reference clone, detached at base tip (never dirtied)
  isolas/<task>/<repo>/    # per-task isolated worktrees — where agents actually work
  .wi/                     # machine state (git-ignored)
    state/                 # per-task isolate registry
    land/                  # durable parked-land records (resume after a block/crash)
    journal/               # append-only op journal → offline roll-forward recovery
    locks/                 # liveness-aware advisory locks (pid/host/boot_id)
    mirrors/               # cached fetch-freshness snapshots (no network on read)
    log/                   # one JSONL trace per op_id
```

The manifest (`wi.config.jsonc`) is JSON with `//` comments:

```jsonc
{
  "defaults": { "base": "main" },     // inherited by repos that omit their own base
  "repos": [
    { "name": "api", "url": "https://github.com/you/api.git" },
    { "name": "web", "url": "https://github.com/you/web.git", "base": "develop" }
  ]
}
```

---

## Command reference

| Command | What it does |
|---|---|
| `wi init` | Create the `.wi/` skeleton + `wi.config.jsonc` in the current directory |
| `wi repo add <name> <url> [--base <branch>]` | Register a source repository in the manifest |
| `wi sync [<repo>…]` | Fetch declared repos into their local SSOT mirror (all if none named) |
| `wi isolate new <task> <repo>…` | Materialize an isolated worktree set for a task |
| `wi resolve <task>` | Print the path bundle for a task's isolate |
| `wi land <task> <repo>… [--atomic]` | Fast-forward each repo's isolate work onto its base branch |
| `wi land status <task>` | Show a parked land's per-repo phase (landed / blocked / pending) |
| `wi land continue <task>` | Resume a parked land, re-attempting each blocked repo |
| `wi land abort <task>` | Undo a parked or completed land, rewinding each base to pre-land state |
| `wi isolate rm <task> [<repo>…]` | Remove a task's isolate and release its worktrees |
| `wi isolate repair <task>` | Reconcile an isolate with its on-disk worktrees + ownership markers |
| `wi gc [--dry-run]` | Reclaim leftover worktrees `wi` can prove it owns |
| `wi state cas <ns> <key> --expected <v\|__ABSENT__> --new <v>` | Atomic compare-and-swap on a namespaced key (agent coordination) |
| `wi lock ls` | List workspace locks and whether each is safe to break *(unix)* |
| `wi lock break <key>` | Displace a stale lock — only when its holder is proven dead *(unix)* |
| `wi help [topic]` | Self-describing command catalog (great for agents) |

**Global flags** (accepted in any position): `--dry-run`, `--format {json\|text}`.

---

## The machine contract

Every command — success or failure — prints exactly **one JSON envelope** to stdout:

```json
{
  "schema_version": "1.1",
  "capabilities": ["help-json","resolve-block","dry-run","partial-success","land","land-atomic","state-kv"],
  "op_id": "op_mr0ktqkr_iok725gp",
  "command": "isolate new",
  "ok": true,
  "action": "created",
  "dry_run": false,
  "repos": [{ "repo": "api", "action": "created" }],
  "warnings": [],
  "next": ["wi resolve add-checkout"],
  "error": null
}
```

On failure, `ok` is `false` and `error` carries a **closed** `kind` plus a stable
sub-`code`, a human `message`, and runnable `did_you_mean` / `help` hints. **Agents
branch on `error.kind` and the exit code — never on message text.**

### Exit codes

| Code | Meaning |
|---|---|
| `0` | success — includes every `--dry-run`, `help`, and `noop` |
| `2` | durable, resumable **partial** success (some repos done, one parked) |
| `3` | `not_found` — unknown repo / task / help topic |
| `4` | `conflict` / `dirty_worktree` / `already_exists` — refused before any write |
| `5` | `needs_approval` — reserved for hooks/approvals (v2) |
| `6` | `lock_held` (transient contention), or `mirror_stale` on the land path |
| `64` | `usage` — bad args, unknown flag, malformed manifest |
| `70` | `internal` — infrastructure/I-O failure |
| `130` | interrupted (SIGINT) |

The full enum/exit/action vocabulary is frozen in `internal/contract` and guarded by
`contract.lock.json` + `TestContractFrozen`.

---

## Using `wi` as a Claude Code skill

`wi` ships with a Claude Code skill so an agent **automatically isolates a workspace
when it starts a new task** instead of editing repos in place. See
[`skill/SKILL.md`](skill/SKILL.md) for the source and [`skill/INSTALL.md`](skill/INSTALL.md)
for the two-step install (copy the skill into `~/.claude/skills/wi/` and register a `/wi`
line in your global `CLAUDE.md`). Once installed, "isolate this work" / "start a new task
in parallel" triggers the full `wi isolate new … → work → wi land` flow, with the agent
parsing the JSON envelope at every step.

---

## Design & invariants

`wi` holds a short list of non-negotiable invariants (see [`DESIGN.md`](DESIGN.md) §2):
no LLM dependency, no hidden network, SSOT stays pristine (detached-HEAD + `update-ref`
only), base mutation is fast-forward-only, reclamation is evidence-positive, and every
command emits exactly one envelope. The build is fitness-test-first: every guard ships a
paired non-vacuity mutant, and the full closed-enum contract is drift-tested in CI.

The architecture and rationale live in [`DESIGN.md`](DESIGN.md); the milestone build
plan lives in [`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md).

---

## Status

- **MVP (M0–M3): complete and green** — the full `init → repo add → sync → isolate new
  → resolve → isolate rm` pipeline, the frozen wire contract, and CI (linux + macOS).
- **v1 (M4): in progress** — `land` / `land continue|abort|status`, `gc`, liveness-aware
  locks, durable journal + crash recovery, and `isolate repair` are all in place. The
  remaining M4 item is `wi doctor` / `wi check` (read-only health diagnosis with a
  bounded `--fix`).
- **M5** (agent ergonomics: `step`/`ports`/`hooks`) is deferred past v1.

## License

Not yet chosen — add a `LICENSE` before publishing (MIT or Apache-2.0 recommended).
