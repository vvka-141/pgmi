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
reaches the basic and advanced templates (minimal writes no skills): the pgmi skills (workflow, SQL, testing, metadata,
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

## Sources

The templates ship under MIT, so anything in a recipe is something every user
may copy and redistribute. Only pgmi's own text and code can be licensed that
way: a recipe's prose, schema, data and test are pgmi's own.

PostgreSQL behavior is cited to the official documentation, the primary
source, and proven by the recipe's runnable test. Book attribution is not
used: no recipe names a book, chapter or author, and no book text is pasted
into an AI prompt to draft one. A direct quotation, if ever needed, is short,
marked and cited.

Each recipe's Source section has two parts: the PostgreSQL documentation
sections it relies on, and the *pgmi adaptation* (the schema, the layer
placement, the test).

## What is removed

`pgmi ai skills`, `pgmi ai skill`, `pgmi ai setup`, `pgmi ai check`, `pgmi ai client`, the
adapter layer, the managed-file stamp, the MCP `ai_skills`/`ai_skill` tools and
the embedded skill sync list. `pgmi ai` (overview) and `pgmi ai contract` stay.

## How success is measured

**Tasks.** Each run gives one coding agent one short task on a pgmi project
and a throwaway PostgreSQL 18 database. The agent starts a new session with
no earlier conversation and no user-level instructions, skills or plugins;
what it knows about pgmi comes from the installed binary and the project's
`AGENTS.md` and skills. It may run any
command, deploy as often as it likes and fix its own mistakes; no human
intervenes. The task set:

| tasks | template | runs | what the agent gets |
|---|---|---|---|
| entity with REST, MCP tool, room bookings, multi-tenant app | advanced | 8 | a fresh scaffold |
| audit a table, lock-safe index, unique visitors per day | basic | 12 | a fresh scaffold |
| the pgmi project inside a TypeScript or .NET app | advanced | 8 | a workspace with `db/` |
| inline test, nested fixture, JSON seed, explain a deploy | basic | 4 | a fresh scaffold, small model |
| a library app | none named | 4 | an empty directory |
| due dates on tasks | basic | 4 | a deployed project whose database holds data |
| reading-list app, then a second feature | advanced | 2 | an empty directory, then a second agent on the same project and database |

Claude Code (claude-opus-5-5; claude-haiku-5-5 for the small-model tasks) and
Codex (gpt-6.1-sol) each ran every task except the small-model ones, which
only Claude ran. A run is one task; the last row is one run of two sessions.

**Assessment.** After the agent stops, the harness deploys the result into a
new database and deploys it again, checks that the agent added tests and that
they ran and passed, and runs a task probe: SQL against the deployed database
that checks the requirement itself, such as a partial unique index enforcing
one open loan per book, the kept rows still present, or no table losing rows
between sessions. It then counts checklist defects, comparing against a fresh
scaffold of the same template so the template's own objects never count:
row-by-row loops a set operation could replace, validation in code that a
constraint could express, a foreign key without an index, a project that
fails to redeploy, new functions executable by PUBLIC, and pgmi identifiers
that do not exist. The first two are judged by reading the code; the rest
are SQL or text checks.

**Results** (binary `7ebeaab25054`, 2026-11-05, 42 runs):

- The final result of every one of the 41 implementation runs deployed,
  redeployed, ran its own tests successfully, passed its task probe and had
  no checklist defect. The 42nd run, the explain-only task, answered
  accurately.
- With an empty directory, all 4 agents ran `pgmi init` themselves and chose
  the basic template; none kept the minimal default.
- On a database that already held data, all 4 agents added a new run-once
  file instead of editing one that had already run, and kept the data.
- On the advanced template, both session-1 agents built the app with REST
  routes, an MCP tool and tests, and served it through the Python reference
  gateway (`GET /api/books` answered 200). Both session-2 agents extended it
  on the same database without any table losing a row.
- 26 of 42 runs called `pgmi ai contract` (all 19 Codex runs, 7 of 23 Claude
  runs), so both `pgmi ai` commands stay.
- In 3 runs the first deploy was refused because a route marked `readOnly`
  still had the default `autoLog: true`; each agent fixed it from the
  error's hint and redeployed.

**Limits.** One or two runs per agent and task; the advanced two-session
workflow ran once per agent. The tasks are short (median 2.4 minutes, longest
14). The checklist catches the defects listed above, not every design flaw.

**Earlier comparison.** On 2026-11-01, an earlier task set ran on v0.13.1 and
on a v0.14 candidate before the basic template was redesigned: runs with at
least one checklist defect fell from 7 of 20 to 0 of 20. Four of those 7 runs
had new functions executable by PUBLIC, which the basic template's deploy.sql
now revokes by itself, so that part of the drop comes from the template, not
the agents. Because the tasks and the template both changed, that comparison
cannot isolate what the guidance alone contributed.

Results and transcripts are kept by the maintainer and are available on
request.

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
