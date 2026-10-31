---
title: "AI guidance ships with the template"
description: "Design record: pgmi scaffolds portable, project-owned agent skills per template instead of rendering guidance into each assistant's format, and keeps only version-coupled facts in the CLI."
weight: 40
---

# Design Record: AI Guidance Ships With the Template

> **Status: PROPOSED** — target: the release after v0.13.1.

## Decision

Agent guidance is scaffolded with the project, per template, in the portable
Agent Skills format. After `pgmi init` it belongs to the project, exactly like
`deploy.sql`. The CLI keeps only what must match the installed binary.

| Guidance | Where it lives | Who owns it |
|---|---|---|
| Session API, `pgmi_test()` macro, execution contract, exit codes | `pgmi ai contract`, `pgmi ai` | the binary |
| Template conventions: layering, handlers, tests, sort keys | `.agents/skills/` in the scaffold | the project |
| PostgreSQL design recipes | `.agents/skills/` in the scaffold | the project |

## Why

**Nine adapters is a maintenance surface pgmi should not own.** `pgmi ai setup`
renders the same guidance for Claude, Codex, Cursor, Copilot, Windsurf, Cline,
Gemini, Antigravity and AGENTS.md. Every vendor format change becomes a pgmi
bug. The industry converged on two portable surfaces instead: `SKILL.md`
folders (the Agent Skills specification at agentskills.io, published December
2025) and `AGENTS.md`.

**The guidance is template-specific.** Handler patterns, the endpoint recipe and
MCP conventions describe the advanced template's code. Shipping them to a basic
project misleads the agent. One skill (`pgmi-templates`) is for pgmi
contributors and misleads everyone.

**Conventions are the project's to change.** A team that renames its schemas or
changes its test idiom must be able to edit the skill that teaches it. A
binary-managed file with a checksum stamp forbids exactly that.

**Version-coupled facts must not be copied.** A scaffolded copy of the session
API goes stale on the next upgrade. `pgmi ai contract` is rendered from
`schema.sql` and `api-v1.sql` and is drift-tested. Scaffolded skills point to
it instead of restating it.

## Rejected: a generic format that agents convert

Shipping "principal" skills and asking each agent to convert them into its
native format was considered and rejected:

- the conversion is non-deterministic and untestable;
- it creates two copies, and an edit to one silently orphans the other;
- the portable format already exists.

## Layout

```
AGENTS.md                 routing table: "before editing X, read skill Y";
                          "run pgmi ai contract for the session API"
CLAUDE.md                 @AGENTS.md
.agents/skills/
  <template skills>/SKILL.md
  postgres-<area>/SKILL.md
  postgres-<area>/references/<recipe>.md
```

The routing table in `AGENTS.md` does not depend on any harness discovering
`.agents/skills/` on its own. Harnesses that do discover it get implicit
loading as a bonus. Claude Code reads `CLAUDE.md`, which imports `AGENTS.md`.

## PostgreSQL recipes

Agents know textbook normalization. They do not reach for PostgreSQL's own
features: exclusion constraints, partial unique indexes as integrity rules,
HyperLogLog for cardinality, logical decoding for change capture, range types,
`LISTEN/NOTIFY`, advisory locks. The recipe library closes that gap.

**Many short recipes, few skills.** Every skill's name and description sits in
the agent's context at startup, and trigger accuracy falls as near-duplicate
skills accumulate. Recipes are therefore grouped into roughly ten skills by
decision area (integrity, indexing, change capture, analytics, concurrency,
time, security, ...). Each `SKILL.md` is a short index: "facing X, read
`references/Y.md`". Each recipe is one reference file, loaded only when needed.

**Every recipe has the same shape:**

| Section | Content |
|---|---|
| Problem | the situation, in the agent's words |
| Use when / Don't use when | the decision boundary |
| Requires | minimum PostgreSQL version; extension, and whether managed services offer it |
| Pattern | the SQL |
| Proof | a pgmi test that demonstrates the property |
| Pitfalls | what goes wrong in production |

**Every recipe's SQL runs in CI.** A recipe that has never executed is a recipe
an agent copies into a broken deploy. The runnable blocks are deployed into a
scaffolded project against a real PostgreSQL in the test suite.

## Sources: credited, never copied

Some recipes draw on ideas from Dimitri Fontaine's *The Art of PostgreSQL* and
apply them inside pgmi's own designs (the membership schema, the three-layer
architecture, the test idiom), which are pgmi's and not taken from the book.
The templates ship under MIT, so anything in a recipe is something
every user may copy and redistribute. Only pgmi's own text and code can be
licensed that way.

**What a recipe may take from the book:** ideas, techniques, the reasoning for
when a feature fits. These are not anyone's property.

**What a recipe never takes:** prose, code listings, example schemas and data
sets, or a chapter's sequence of points restated in new words. No book text is
pasted into an AI prompt to draft a recipe; an agent asked to "rewrite" a
passage reproduces it.

**How a recipe is written:** read the chapter, close it, write the recipe
against a pgmi schema, then compare the result with the chapter for anything
that tracks it too closely. PostgreSQL behavior is cited to the official
documentation, which is the primary source; the book is cited where its
framing shaped the recipe.

**Attribution says exactly what was borrowed.** Each recipe's Source section
has two parts:

- *Drawn from:* the book and chapter, and the idea taken.
- *pgmi adaptation:* what pgmi added — the schema, the layer placement, the
  test. The three-layer architecture is pgmi's and is never attributed to the
  book.

A recipe that owes nothing to the book does not cite it. The book is never used
to imply endorsement: no "based on The Art of PostgreSQL" in release notes,
the README or marketing. If a recipe ever needs a direct quotation, it is short,
marked as a quotation, and cited.

## What is removed

`pgmi ai skills`, `pgmi ai skill`, `pgmi ai setup`, `pgmi ai check`, the
adapter layer, the managed-file stamp, the MCP `ai_skills`/`ai_skill` tools and
the embedded skill sync list. `pgmi ai` (overview) and `pgmi ai contract` stay.

## How success is measured

A fixed task set ("add an entity with a REST endpoint and tests", "deduplicate
visitors per day", "audit every change to a table") runs against a scaffolded
project with a cold agent, before and after. The measure is defects in the
result, and whether the agent called `pgmi ai contract` at all. If it does not,
even with the `AGENTS.md` pointer, `pgmi ai` shrinks further.

## Non-goals

- Keeping scaffolded skills in sync with later pgmi releases. They are project
  source; upgrades change only what the CLI prints.
- Per-harness tuning of any kind.
