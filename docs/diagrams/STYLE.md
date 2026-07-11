# Diagram Style Guide

Conventions for all diagrams in this directory. The palette derives from the
website brand palette (`website/assets/landing.css`), so embedded diagrams read
as native to the docs site.

## Files

- Source of truth: `<nn>-<slug>.drawio` (native draw.io XML, committed)
- Published asset: `<nn>-<slug>.drawio.svg` (exported with embedded XML, committed)
- Regenerate all SVGs: `make diagrams` (requires draw.io Desktop)
- Never hand-edit an exported `.svg`; edit the `.drawio` and re-export

## Palette

| Role | Fill | Stroke |
|------|------|--------|
| Background (pinned, never transparent) | `#fbfaf7` | — |
| Text | — | ink `#172018` |
| Section labels / captions | — | muted `#5d665d` |
| pgmi-owned components | `#eaf1e7` | forest `#143d2b` |
| PostgreSQL / session elements | `#d9f4e8` | teal `#0f766e` |
| Control handover / "your SQL" accent | amber `#b45309` fill with white text, or amber stroke | `#b45309` |
| External / not-owned systems | `#f5f5f5` | `#d9ded5`, dashed |
| Dividers, band borders | — | line `#d9ded5` |

Amber is reserved for the single most important element per diagram — the
moment control passes to the user's SQL. If a diagram needs amber twice,
it is two diagrams.

## Layout

- Layered horizontal bands, top-to-bottom flow; small uppercase muted labels
  (`fontSize=11`, `fontStyle=1`, color `#5d665d`) above each band
- Rounded rectangles (`rounded=1`), `fontSize=12` body text
- Dashed borders mean "external, not owned by pgmi"
- Edges: solid, `endArrow=classic`; color follows the semantic of the flow
  (forest for pgmi actions, amber for the handover, muted for external)
- Pin the page background: `background="#fbfaf7"` on `mxGraphModel` so exports
  are identical on GitHub, Hugo light, and Hugo dark

## Icons

`icons/` holds small stroke-based SVGs in palette colors. Use icons only for
external actors (database, terminal, CI, HTTP client, AI agent) — never inside
pgmi-owned boxes. Boxes, arrows, and typography carry the diagram.

## Content rules

- One diagram, one stable concept; no CLI flag lists or volatile file names
- Identifiers shown in diagrams (views, functions, step types) must match
  `internal/params/schema.sql` / `internal/contract/api-v1.sql` exactly
- Diagrams are documentation: they are in scope for coherence review
