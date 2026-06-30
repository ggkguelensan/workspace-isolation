# wi — brand / logomark

The `wi` mark is the project's git-graph motif folded into the wordmark: a **base-ref
line** runs across the top, the **dot of the `i` is a cyan commit node** riding that
line, an **amber tick** marks where work lands by fast-forward, and a quiet **grounding
stroke** sits beneath. It says, in one glyph, what `wi` does — isolate a node off a
base ref, then land it forward.

![the wi logomark](logo.svg)

## Files

| File | Use |
|---|---|
| [`logo.svg`](logo.svg) | **Primary lockup** (460×286). READMEs, docs, slides — anywhere with horizontal room. |
| [`logo-icon.svg`](logo-icon.svg) | **Square icon** (320×320, squircle). Favicon, GitHub avatar / social, app icon — legible down to 32px. |

Both are self-contained SVGs: no external fonts, no filters, librsvg-safe. The wordmark
is set in a monospace stack (`ui-monospace, 'SF Mono', Menlo, Consolas, monospace`,
weight 700); the `i` stem and dot are drawn as shapes so the tittle can land exactly on
the base-ref line regardless of the rendering font.

## How to read it

- **Base-ref line** — the trunk the work rides; cyan, brightest at the node, fading at
  both ends (a ref has no end, only a current position).
- **`i`-dot = commit node** — the isolated worktree node, sitting *on* base.
- **Amber tick** — the single accent, used exactly once: the land point further along
  the line, where the node fast-forwards back onto base.
- **Grounding stroke** — the quiet underline that seats the wordmark.

## Palette

| Token | Hex | Role |
|---|---|---|
| Ground (top→bottom) | `#0A1222` → `#0B1A33` | Navy badge fill |
| Cyan node / ref bright | `#54C7E8` | Commit node, line peak |
| Cyan ref dim | `#2E6E88` | Line ends, grounding stroke |
| Amber accent | `#F2B43C` | Land tick — **once only** |
| Text primary | `#E2ECF7` | Wordmark |

## Usage

- Keep the amber to a **single** accent — it means "land", not decoration.
- Don't recolor the node off-cyan or the accent off-amber; the two-colour logic *is* the
  brand (cyan = structure, amber = the one privileged operation).
- Give the mark clear space ≈ the height of the `w`; don't crowd it.
- On light backgrounds, place the badge as-is (it carries its own navy ground) rather
  than knocking the wordmark out to dark.

The mark shares its palette and mono + sans type pairing with the project
[`banner.svg`](../banner.svg).
