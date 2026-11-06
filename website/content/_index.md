---
title: pgmi
description: "Your deploy.sql controls the PostgreSQL deployment and tests the changes before COMMIT. A failing test aborts the deployment transaction."
---

# pgmi

**PostgreSQL deployments written in SQL, tested before they commit.**

pgmi loads your project files into one PostgreSQL session as queryable data,
then runs the `deploy.sql` you write. Your deployment selects migrations, loads
reference data, tests the changed database, and commits only if the tests
pass. A failing test aborts the deployment transaction.

Migration frameworks decide ordering, history and the transaction model for
you. With pgmi those decisions are SQL in your `deploy.sql`
([Why pgmi?]({{< relref "docs/why-pgmi" >}})).

![Test-gated deployment: apply files, test the changed database, commit only if tests pass](docs/diagrams/d00-test-gated-deploy.drawio.svg)

## Start a project

`pgmi init` writes one of three starting points:

| | Writes | For |
|---|---|---|
| **minimal** (the default) | `deploy.sql`, a loop that runs every SQL file in path order, and `hello_world.sql` | Building your own deployment from nothing |
| **basic** | `physical/` tables that run once, tracked in a ledger that refuses an edited file; `semantic/` views and functions re-run on every deploy; tests that gate the commit | Deploying a schema with a small project. PostgreSQL 15+ |
| **advanced** | Your application's backend in PostgreSQL, as code you own: HTTP API, sign-in, tenancy and audit, served by reference gateways | Building the application itself. PostgreSQL 18+; read [the advanced-template guide]({{< relref "docs/advanced" >}}) first |

The basic and advanced templates also write an `AGENTS.md` and skills into the
project, so a coding agent learns the project's conventions before it edits.

## Try it

```bash
# 1. Install (macOS / Linux, no Go toolchain needed)
curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | bash

# 2. Scaffold a project
pgmi init myapp --template basic
cd myapp

# 3. Deploy to your local PostgreSQL
pgmi deploy . --overwrite --force --param env=dev
```

Windows PowerShell install: `irm https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.ps1 | iex`.
The full walkthrough, including a test that fails and rolls the deploy back, is
the [Quickstart]({{< relref "docs/quickstart" >}}).

## Go deeper

- [Articles]({{< relref "articles" >}}): test gates, transaction boundaries and deployments as PostgreSQL programs
- [Why pgmi]({{< relref "docs/why-pgmi" >}}): the approach, and when it does not fit ([Tradeoffs]({{< relref "docs/tradeoffs" >}}))
- [Documentation]({{< relref "docs" >}}): the full reference
- [GitHub](https://github.com/vvka-141/pgmi): source, releases, issues
