---
title: "AI guidance ships with the template"
description: "Design record: pgmi scaffolds portable, project-owned agent skills per template instead of rendering guidance into each assistant's format, and keeps only version-coupled facts in the CLI."
weight: 40
---

# Design Record: AI Guidance Ships With the Template

> **Status: ACCEPTED** — shipped in v0.14.0.

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
gateways/                 advanced template only: reference gateways
  README.md, PROTOCOL.md  (TypeScript, Python, Go, C#, Rust); never deployed
```

In the pgmi repository the skills come from two trees. `internal/scaffold/skills/`
reaches every template: the pgmi skills (workflow, SQL, testing, metadata,
debugging a deploy, design, the physical and semantic layers) and the
`postgres-<area>` recipes.
`internal/scaffold/templates/<name>/.agents/skills/` reaches one template:
the advanced template adds `pgmi-endpoint`, `pgmi-api-layer` (its layers and
the API on top), `pgmi-handlers`, `pgmi-api` and `pgmi-mcp`. Skills for pgmi's own contributors (`pgmi-templates`, the review
skills) are never scaffolded.

Both templates ship the shared skills; the basic template ships no
template skills of its own. The advanced template's `gateways/` folder holds
reference gateways, not skills: `AGENTS.md` sends an agent there before it
builds a client, gateway or service, and `.pgmiignore` keeps it out of the
deploy. It replaces `pgmi ai client`.

**Workspace root.** Instruction files load only for the folder an agent works
in. `AGENTS.md` asks the agent to add one line to the workspace root's
`AGENTS.md` that points at the PostgreSQL project's own `AGENTS.md` and its
`gateways/`, so a session started at the workspace root still finds them.

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

A runnable block is a fenced block whose info string is
`sql runnable target=<path>`: the gate (`TestSkillBlocksDeploy`) writes it to
`<path>` in each template that ships the skill and deploys the project with its
tests. `kind=tail` appends it to `deploy.sql` after the first top-level `COMMIT`
instead, for statements that cannot run inside a plan file.
`requires=<extension>` skips it, with the reason logged, on a server that lacks
the extension. A block counts only if it provably ran: a target that no part
of `deploy.sql` executes fails the gate. Shared skills also run on the oldest
PostgreSQL release CI tests; template skills run on that template's floor.
Plain `sql` blocks are illustration.

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

`pgmi ai skills`, `pgmi ai skill`, `pgmi ai setup`, `pgmi ai check`, `pgmi ai client`, the
adapter layer, the managed-file stamp, the MCP `ai_skills`/`ai_skill` tools and
the embedded skill sync list. `pgmi ai` (overview) and `pgmi ai contract` stay.

## How success is measured

A fixed task set ("add an entity with a REST endpoint and tests", "deduplicate
visitors per day", "audit every change to a table") runs against a scaffolded
project with a cold agent, before and after. The measure is defects in the
result, and whether the agent called `pgmi ai contract` at all. If it does not,
even with the `AGENTS.md` pointer, `pgmi ai` shrinks further.

Measured for v0.14.0 against v0.13.1, on the same twenty runs (Claude Code
and Codex, cold, scaffold guidance only): every run deploys into a fresh
database and passes its own tests (19 of 20 before), and runs with a
checklist defect fall from seven to none (four of the seven were functions
left executable by PUBLIC, which the basic template now revokes itself). Agents ran `pgmi ai contract` in 20 of 32
runs, against none before, so both `pgmi ai` commands stay. Agents started
at a workspace root found the project's `AGENTS.md` and `gateways/`, and a
small model completed the three testing tasks on the first attempt.

## Non-goals

- Keeping scaffolded skills in sync with later pgmi releases. They are project
  source; upgrades change only what the CLI prints.
- Per-harness tuning of any kind.
- A skill for running a pgmi project inside the browser on PGlite. It needs
  a second implementation of pgmi's session contract (the session views, the
  plan, the test macro) in TypeScript, which pgmi's tests do not hold to the
  contract and which drifts with every release. The advanced template's
  roles, login sessions and row-level security protect nothing in a
  single-user browser database, so the template would have to be cut down to
  run there. Revisit only if pgmi ships and tests such a runtime itself.
