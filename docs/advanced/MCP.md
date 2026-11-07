---
title: "MCP gateway overview"
description: "Understand the advanced template's Model Context Protocol support: what is scaffolded, what you operate, and where to start."
weight: 30
url: /docs/mcp/
---

# Advanced Template: MCP Gateway

> **Scope: advanced template only.** This subsystem is scaffolded by
> `pgmi init --template advanced`. It is not part of pgmi core and is not
> included in the basic template. The generated SQL and gateway become
> application code that you own.

pgmi has **two unrelated MCP surfaces** — be sure you're reading about the right one:

- **`pgmi serve`** (part of the CLI, every install): exposes pgmi's own commands (init, metadata, deploy) as an MCP server over stdio, so a coding agent that develops the project can drive pgmi itself. See the [CLI reference](../CLI.md#pgmi-serve).
- **The advanced-template MCP gateway** (this section): exposes *your deployed PostgreSQL application* — its tools, resources, and prompts — to AI assistants over HTTP.

The Model Context Protocol (MCP) is an open standard that allows AI
applications (Claude Desktop, VS Code Copilot, etc.) to connect to external
systems. The advanced template implements MCP entirely in PostgreSQL, with a
thin HTTP gateway for transport.

![One MCP tool call: the gateway verifies the agent's token or connection handle and asks the request policy, then api.mcp_handle_request resolves the caller and dispatches to your tool handler, kernels, tables and api.mcp_exchange inside one transaction the gateway commits](../diagrams/d15-template-mcp-gateway.drawio.svg)

## What is scaffolded

- **SQL-side protocol implementation** — a JSON-RPC 2.0 dispatcher
  (`api.mcp_handle_request`) that serves both MCP eras on one endpoint:
  stateless MCP 2026-07-28 (`server/discover`, the version in every request's
  `_meta`) and the legacy `initialize` handshake that current mainstream
  clients still send. Typed handler registration, discovery, and
  response builders, all inside your database. Tool discovery is auth-aware:
  `api.mcp_list_tools` hides the tools a caller may not call from that
  session, so an agent's visible capability set is scoped to its identity.
  See the [SQL API reference](MCP-SQL-API.md).
- **Five HTTP gateways** — reference gateways in Python, TypeScript, Go, C#
  and Rust under `gateways/`, each bridging HTTP POST to the SQL dispatcher
  with transaction-policy resolution and serialization-failure retries. Each
  also signs people in, serves REST (the
  [reference login transport](LOGIN-TRANSPORT.md#the-reference-transport)),
  acts as an OAuth resource server for MCP clients, and offers two extension
  seams: a database channel and an interceptor pipeline. They keep one
  written protocol (`gateways/PROTOCOL.md`) and pass the same end-to-end
  tests. See [Run the gateway](MCP-GATEWAY.md).
- **Agents with their own identity** — an MCP caller is the agent of a
  connection a person approved while signed in on the web. The agent has its
  own principal and an `agent.*` role; it acts for the person with the
  role's permissions intersected with theirs, in the organization chosen at
  approval. Every log records both the person and the agent. The client
  signs in with OAuth (the first call answers a pairing code the person
  approves) or sends an approved handle. Nothing on MCP provisions anyone.
  See [MCP identity](MCP-GATEWAY.md#mcp-identity).
- **Handler recipes** — complete, copy-ready examples for tools, resources,
  and prompts in [Writing MCP Handlers](MCP-HANDLERS.md). The template ships
  REST examples and one MCP tool, `echo_note`, in `api/examples.sql`; add
  your own MCP handlers the same way.

## Agents as callers

An agent calls your application's operations, not its tables. Once one call
commits, a later call sees that state, and its kernel can refuse it: an agent
that asks for the last seat after another agent booked it gets a refusal it
can read. What the template enforces for every tool call:

- The call is one transaction, at the isolation the tool declared. It
  commits whole or rolls back.
- The agent's access is checked in the database before the handler runs:
  its own principal, its `agent.*` role intersected with its person's
  permissions, and row-level security on every row it touches.
- Discovery is scoped: `api.mcp_list_tools` lists only the tools that session
  may call.
- The exchange log names both the agent and the person it acts for.

Your kernels supply the business rules. For calls that overlap, the declared
isolation decides. At the default `READ COMMITTED`, a precondition holds
across overlapping calls only through a row lock or a constraint. Under
`SERIALIZABLE` one of them fails with a serialization error, the gateway
retries it as a whole, and the retry sees the other's result.

How a tool answers a refusal is your design. The scaffolded `pgmi-mcp` skill teaches a refusal shape (`code`, `rule`, `remedy`) that an
agent can act on, with a runnable example; the template itself ships only
`echo_note`.

## What you operate

The gateways are reference implementations: read the one in your language,
then write or adapt your own and run it behind your infrastructure (TLS,
reverse proxy, pooling). They are not a managed pgmi service. The SQL handlers are ordinary functions in
your project tree, deployed and tested like everything else.

## What is intentionally missing

- **No pagination** — `mcp_list_*` return the full list in one call.
- **No `listChanged`** — lists carry `ttlMs` and `cacheScope`; a client lists
  again when they run out.
- **No `input_required`** — the request is the transaction boundary, so every
  tool answers in one round trip.
- **No server-initiated SSE stream** — `GET /mcp` answers 405; the gateways
  emit no server-initiated notifications.

Details and the compliance surface are on the
[protocol page](MCP-PROTOCOL.md).

## Where to start

| You want to | Read |
|---|---|
| Deploy, run, and connect an AI client | [Run the MCP gateway](MCP-GATEWAY.md) |
| Write tools, resources, and prompts | [Author MCP handlers](MCP-HANDLERS.md) |
| Look up dispatcher functions and response builders | [MCP SQL API reference](MCP-SQL-API.md) |
| Check protocol versions, both eras with curl examples, transport behavior, limitations | [Protocol compliance & limitations](MCP-PROTOCOL.md) |

## See Also

- [Advanced template overview](README.md) — the whole application stack
- [Session API](../session-api.md) — pgmi core session tables and functions
