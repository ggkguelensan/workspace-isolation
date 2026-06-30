---
name: wi
description: "Deterministic multi-repo workspace isolation for parallel agents. Use whenever you START WORK ON A NEW TASK that touches one or more git repositories — instead of editing repos in place, isolate the task into its own worktrees, work there, then land it back. Triggers: `/wi`, 'start a new task', 'isolate this work', 'work on X in isolation', 'spin up a workspace for', 'parallel agents on the same repo(s)', or any request to create/land/clean up an isolated multi-repo working set. Drives the `wi` CLI (init / repo add / sync / isolate new / resolve / land / gc) and parses its JSON envelopes."
---

# /wi

Deterministic, crash-safe **workspace isolation** for working on a task across one or
more git repositories without disturbing the originals — so several agents can work in
parallel on the same repos. `wi` is a single binary that speaks one JSON envelope per
command; you are the brain, `wi` is the deterministic hands.

## Usage

```
/wi init                                  # create the wi workspace skeleton in the current dir
/wi repo add <name> <url> [--base <b>]    # register a source repository in the manifest
/wi sync [<repo>…]                        # fetch declared repos into the local SSOT mirror
/wi isolate new <task> <repo>…            # materialize isolated worktrees for a task  ← START HERE for new work
/wi resolve <task>                        # print the path bundle (where to cd and work)
/wi land <task> <repo>…                   # fast-forward the task's work onto each base branch
/wi land status <task>                    # show a parked land's per-repo phase
/wi land continue <task>                  # resume a parked land after fixing a blocked repo
/wi land abort <task>                     # undo a parked/completed land, rewind to pre-land state
/wi isolate rm <task> [<repo>…]           # remove the isolate, release its worktrees
/wi isolate repair <task>                 # reconcile an isolate with its on-disk worktrees
/wi gc [--dry-run]                        # reclaim leftover worktrees wi can prove it owns
/wi state cas <ns> <key> --expected <v|__ABSENT__> --new <v>   # atomic key claim (coordination)
/wi lock ls | /wi lock break <key>        # inspect / break stale locks (unix)
/wi help [topic]                          # self-describing command catalog
```

Global flags (any position): `--dry-run` (plan, no side effects, exits 0), `--format {json|text}`.

## What wi is for

When you are about to **start a new task** that changes code in one or more repos, do
not edit the repos in place — isolate the task first. `wi` cuts a fresh set of git
worktrees from a pristine local mirror, you do all your work there, and when you're done
`wi land` fast-forwards it back onto the base branch. This keeps the originals clean,
lets multiple tasks/agents run at once, and is crash-safe (durable journal + resumable
parked lands + evidence-positive cleanup).

## What You Must Do When Invoked

If invoked as `/wi --help` or `/wi -h` with no other args, print the **Usage** block
above verbatim and stop.

`wi` is a CLI on `PATH`. Run it with Bash (`wi <command> …`). **Every command prints one
JSON envelope to stdout** — always parse it; never branch on human message text. If `wi`
is not found, tell the user to install it (`go install ./cmd/wi` from the
workspace-isolation repo, then ensure `~/go/bin` is on `PATH`) and stop.

### Standard flow for new work on a task

1. **Ensure a workspace.** If there is no `wi.config.jsonc` / `.wi/` in the working
   directory, run `wi init`. If the repos the task needs aren't declared yet, add them
   with `wi repo add <name> <url> --base <branch>`, then `wi sync`.

2. **Isolate the task.** Choose a short kebab-case `<task>` name and run:
   ```
   wi isolate new <task> <repo>…
   ```
   Confirm `ok:true` and `action:"created"`. On `error.kind == "already_exists"` the
   task is already isolated — reuse it (go to step 3). On `error.kind == "not_found"` a
   named repo isn't in the manifest — add it and retry.

3. **Resolve and work.** Run `wi resolve <task>` and read the `resolve` path bundle. `cd`
   into each returned worktree path and make all edits/commits **there** — never in
   `repos/<repo>/` (the SSOT mirror) and never in the original repo.

4. **Land the work.** When the task is complete and committed in the worktrees:
   ```
   wi land <task> <repo>…
   ```
   - `ok:true`, `action:"landed"` → done; proceed to cleanup.
   - exit `2` (partial) or a parked record → some repos blocked: go to step 5.
   - exit `6` `mirror_stale` → the base moved; `wi sync` then `wi land continue <task>`.

5. **Handle a parked land.** Run `wi land status <task>` to see which repos are
   `blocked`. For each blocked repo, rebase its worktree onto the updated base, commit,
   then `wi land continue <task>`. If the task should be discarded, `wi land abort
   <task>` rewinds every already-landed repo to its pre-land backup SHA.

6. **Clean up.** When the work is landed (or abandoned), `wi isolate rm <task>` releases
   the worktrees. Use `wi gc --dry-run` first if you want to preview reclamation.

### Reading the envelope

Key fields: `ok` (bool), `action` (created/removed/synced/landed/read/noop), `repos[]`
(per-repo outcome), `warnings[]`, `next[]` (suggested follow-up commands — prefer
running these), `error` (null on success). On failure, branch on **`error.kind`** and
the **exit code**, never the message:

| exit | meaning | what to do |
|---|---|---|
| 0 | ok (incl. dry-run, help, noop) | continue |
| 2 | partial — durable, resumable | inspect `repos[]`; `wi land status`; finish the parked repo |
| 3 | not_found | add the missing repo / fix the task name |
| 4 | conflict / dirty_worktree / already_exists | refused before any write — resolve the cause, retry |
| 6 | lock_held or mirror_stale | retry shortly (lock) or `wi sync` then continue (stale) |
| 64 | usage | fix the arguments |
| 70 | internal | report to the user; do not loop |

Present results to the user in plain language (what was created/landed, what's next).
Show raw JSON only if they ask. Follow `next[]` for the next step when it makes sense.
