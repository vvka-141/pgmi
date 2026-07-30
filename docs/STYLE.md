---
title: "Docs Style Guide"
build:
  render: never
  list: never
---

# Docs Style Guide

Conventions for the prose under `docs/`. Diagram conventions live in
[`diagrams/STYLE.md`](diagrams/STYLE.md).

`docs/` is dual-target: GitHub renders it directly, and `website/hugo.toml`
mounts `../docs` into the site. Anything added here must render on both, which
rules out Hugo shortcodes (`{{%/* hint */%}}` and friends render as raw text on
GitHub — note that even *naming* one here has to be escaped, or Hugo tries to
execute it).

## Documenting the core/template boundary

pgmi the binary and the SQL that `pgmi init` copies into a project are two
different things. A reader cannot tell them apart from prose alone, and the
distinction matters: template SQL is *theirs* — already modified, or deleted.

**The marker is a blockquote, placed immediately under the section heading:**

```markdown
## Runtime security stack

> **Scope: advanced template only.** This subsystem is scaffolded by
> `pgmi init --template advanced`. It is not part of pgmi core.
```

One style, everywhere. A heading may *also* name the template
(`## Passing role passwords (advanced template)`) — that is a title, not a
substitute. Headings are easy to scroll past and invisible to anyone arriving
on a deep link, so the blockquote is what carries the meaning.

### When to mark

Three cases. Classify by reading the section, never by keyword match.

| Case | Marker |
|------|--------|
| Section only applies if you scaffolded a given template | **Blockquote marker**, naming the template |
| Core behaviour illustrated with a template-shaped example | No section marker — name the template in the sentence ("the advanced template does this by…") |
| Core CLI surface that mentions a template because the flag takes one (`pgmi init --template basic`) | No marker — this is core |

If an entire section turns out to be template-only, prefer moving it under
`docs/advanced/` over marking it. Pages already under `docs/advanced/` need no
per-section marker: the section's `_index.md` scopes the whole tree.

### Wording

`> **Scope: <basic|advanced> template only.** <one or two sentences>.`

Say what is scaffolded and that it is not core. Do not hedge — a marker that
says "may not apply" tells the reader nothing they can act on.

## Highlights claims

`docs/HIGHLIGHTS.md` currently describes ten capabilities, each anchored to code
or a guide. Every claim must be re-verified at release time: check the linked
code still exists, the described behaviour still holds, and every competitor
statement is supported by current primary documentation. Avoid absolute
uniqueness claims unless the evidence establishes them. Add this to the
release-checklist item PGMI-175 (post-release sync verification).
