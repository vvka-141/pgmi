---
title: "Diagram Style Guide"
build:
  render: never
  list: never
---

# Diagram Style Guide

Conventions for all diagrams in this directory. The palette derives from the
website brand palette (`website/assets/landing.css`), so embedded diagrams read
as native to the docs site.

## Files

- Source of truth: `<nn>-<slug>.drawio` (native draw.io XML, committed)
- Published asset: `<nn>-<slug>.drawio.svg` (exported with embedded XML, committed)
- `_`-prefixed sources (e.g. `_social-card.drawio`) are skipped by `make diagrams`
  and exported manually to their own target format
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
| External / not-owned systems | `#f5f5f5` | muted `#5d665d`, dashed |
| Dividers, band borders | — | line `#d9ded5` |

Amber is reserved for the single most important element per diagram — the
moment control passes to the user's SQL. If a diagram needs amber twice,
it is two diagrams.

## Layout

Pick the layout by diagram purpose; do not force one layout onto every diagram:

- **Process / pipeline** (e.g. d01): left-to-right flow on a single shared
  center-line — boxes in one logical row must never jog between heights
- **Decision flow** (e.g. d00): top-to-bottom with a rhombus gate and
  symmetric yes/no branches
- **Comparison** (e.g. d02): side-by-side columns of equal width, mirrored
  structure, divider line at the page center
- **Interface mapping** (e.g. d03): two columns across a dashed boundary,
  one-to-one rows horizontally aligned; group derived items explicitly

Common to all: small uppercase muted section labels (`fontSize=11`,
`fontStyle=1`, color `#5d665d`), rounded rectangles (`rounded=1`),
`fontSize=12` body text
- Dashed borders mean "external, not owned by pgmi"
- Edges: solid, `endArrow=classic`; color follows the semantic of the flow
  (forest for pgmi actions, amber for the handover, muted for external)
- Pin the page background: `background="#fbfaf7"` on `mxGraphModel` so exports
  are identical on GitHub, Hugo light, and Hugo dark

## Icons

Two tiers, one rule: icons mark **external actors only** — never inside
pgmi-owned boxes. Boxes, arrows, and typography carry the diagram.

- **Official brand marks** (`icons/logos/`, see ATTRIBUTION.md): use when the
  external product's identity is the point — PostgreSQL, Azure/AWS/GCP in
  connection diagrams, Docker/GitHub in CI contexts. At most a few per
  diagram; a diagram that needs many logos is a slide, not documentation.
- **House stroke icons** (`icons/`): palette-colored generic actors
  (database, terminal, CI, HTTP client, AI agent) for everything else.

Reusable library: `pgmi-icon-library.xml` — in draw.io use *File → Open
Library*; when authoring XML directly, copy the `data:image/svg+xml;base64,…`
payload into an `image=` style (drop the `;base64` marker inside style
strings). All icons embed as data URIs, so diagrams stay self-contained.

## Content rules

- One diagram, one stable concept; no CLI flag lists or volatile file names
- Identifiers shown in diagrams (views, functions, step types) must match
  `internal/params/schema.sql` / `internal/contract/api-v1.sql` exactly
- Diagrams are documentation: they are in scope for coherence review
