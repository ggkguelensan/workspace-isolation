# Installing the `wi` skill into Claude Code

This makes Claude Code reach for `wi` automatically when it starts a new task — and
adds an explicit `/wi` slash command — exactly the way the `graphify` skill works.

## Prerequisites

The `wi` binary must be on your `PATH`:

```bash
go install ./cmd/wi                                  # from the workspace-isolation repo
echo 'export PATH="$(go env GOPATH)/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc
wi help                                              # should print a JSON envelope
```

## Step 1 — copy the skill into your skills directory

```bash
mkdir -p ~/.claude/skills/wi
cp skill/SKILL.md ~/.claude/skills/wi/SKILL.md
```

(Claude Code discovers any `~/.claude/skills/<name>/SKILL.md`; the skill's `description`
front-matter is what triggers it.)

## Step 2 — register it in your global `CLAUDE.md`

Append this block to `~/.claude/CLAUDE.md` (mirrors the `graphify` registration):

```markdown
# wi
- **wi** (`~/.claude/skills/wi/SKILL.md`) - isolate a new task into its own multi-repo worktrees, then land it back. Trigger: `/wi`
When the user types `/wi`, invoke the Skill tool with `skill: "wi"` before doing anything else.
```

## Done

- Type `/wi …` to drive `wi` explicitly.
- Or just say "start a new task on <repos>" / "isolate this work" — the skill's
  description triggers the full `wi isolate new … → resolve → work → land` flow, with the
  agent parsing each JSON envelope.

To update the skill later, re-run Step 1 (it's a plain file copy).
