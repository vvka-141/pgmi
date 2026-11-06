---
title: "PostgreSQL recipe format"
description: "Contributor guide: the shape every PostgreSQL recipe in the scaffolded skills follows, how its SQL is proven in CI, and how its sources are credited."
weight: 41
---

# PostgreSQL recipe format

The scaffold ships PostgreSQL recipes as reference files of the
`postgres-<area>` skills in `internal/scaffold/skills/`. Each skill's
`SKILL.md` is a short index from a situation to a recipe; each recipe is one
file in that skill's `references/`, 40–120 lines. Why the library is shaped
this way: [AI guidance ships with the template](ai-guidance-in-the-scaffold.md).

## Where a recipe goes

Add a recipe to the area whose question it answers (integrity, indexing,
change capture, analytics, concurrency, time, security). Add a row for it to
that skill's index. Start a new area only when no existing one fits, and
route it in both templates' `AGENTS.md`.

The skills in `internal/scaffold/skills/` reach both templates. A recipe there
must hold in a basic project: no advanced-template schemas, roles or
protocols. `TestSharedSkillsNameNoAdvancedOnlyIdentifiers` enforces the
identifiers.

## The file

Copy this skeleton. Every section is required; write "None" where one has
nothing to say.

````markdown
# <What the recipe achieves, as an imperative>

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
The situation in the words an agent would use, and what goes wrong with the
obvious approach.

## Use when
- ...

## Don't use when
- ... (and what to use instead)

## Requires
- PostgreSQL <version>+: the oldest release CI tests (15), or the release
  that introduced the feature if it is newer, checked in the release notes.
- Extension `<name>`: contrib or third-party, and whether the major managed
  services offer it. "None" when core.
- Server settings, such as `wal_level = logical`. "None" when the defaults
  work.

## Pattern
The SQL a project file holds, as a plain `sql` block, against a small
illustrative schema of the recipe's own.

## Proof
A `sql runnable target=__test__/test_<area>_<recipe>.sql` block: a pgmi test
that builds the pattern and demonstrates the property, including that the
wrong input fails.

## Pitfalls
- What goes wrong in production.

## Source
- PostgreSQL documentation: <section title>, <URL under
  https://www.postgresql.org/docs/current/>.
- pgmi adaptation: the schema, the test, where the pattern sits in a project.
````

Recipes are sourced from the PostgreSQL documentation and verified by their
Proof. Book attribution is not used; the Source section names no book, chapter
or author. The rules: [Sources](ai-guidance-in-the-scaffold.md#sources).

## How the Proof runs

`TestSkillBlocksDeploy` writes every runnable block into a fresh scaffold of
both templates and deploys it with the project's tests: the basic template on
the oldest PostgreSQL release CI tests, both templates on the newest.

- A test runs inside a savepoint that is rolled back, so the Proof may create
  tables and extensions. It must not depend on anything the template ships.
- Assert with `RAISE EXCEPTION` under a condition that cannot be NULL. To
  prove a rule rejects bad input, catch the one SQLSTATE you expect inside a
  `BEGIN ... EXCEPTION` block and fail when nothing was raised.
- A statement that cannot run inside a transaction or a function
  (`CREATE INDEX CONCURRENTLY`, `VACUUM`) needs `kind=tail` instead of a
  target. It is appended to `deploy.sql` after the first top-level `COMMIT`
  and must be re-runnable.
- `requires=<extension>` skips the block, with the reason in the test log, on
  a server that lacks the extension. Prefer a Proof that needs no extension.
- A recipe whose feature is newer than the oldest tested release says so in
  Requires, and its Proof checks `current_setting('server_version_num')` and
  returns early below that release.
