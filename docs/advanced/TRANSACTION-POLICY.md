---
title: "Transaction policy"
description: "Per-route transaction isolation, read-only declarations, DEFERRABLE derivation, replica-safe hints, and the retry contract."
weight: 176
---

# Per-route transaction policy

A handler declares what it needs — a minimum transaction isolation floor and
whether the route is read-only — and the system resolves, opens, enforces, and
publishes the resulting policy. No external configuration, no runtime
surprises.

## What a route declares

Two metadata keys on any REST, RPC, or MCP handler:

| Key | Values | Default |
|-----|--------|---------|
| `minTransactionIsolation` | `read committed`, `repeatable read`, `serializable` | none (behaves as `read committed`) |
| `readOnly` | `true` / `false` | `false` (read-write); REST and RPC only. Makes `autoLog` default to `false`; `autoLog: true` with it is refused |

Case- and separator-insensitive: `READ COMMITTED`, `read-committed`, and
`Read_Committed` all normalize to `read committed`. `read uncommitted` folds
onto `read committed`, matching PostgreSQL's behavior — it implements no
dirty-read level.

Unsupported values are rejected at handler registration, not at call time.

Stored on `api.handler.min_transaction_isolation` and `api.handler.read_only`.

Source: `lib/api/00-transaction-isolation.sql` — `internal.normalize_transaction_isolation`.

## Resolve-then-open

The client gateway resolves the policy **before** opening the dispatch
transaction:

```
api.rest_route_policy(method, url, requested)
api.rpc_route_policy(method_name, requested)
api.mcp_request_policy(request, requested, protocol_version, mcp_method, mcp_name)
```

Each returns `(transaction_isolation, transaction_read_only,
transaction_deferrable, handler_name, input_annotations)`; the last two are
what the gateway's interceptors select on (`gateways/PROTOCOL.md`,
"Extension seams"). `api.mcp_request_policy` adds `modern` and `refusal`,
the answer to send instead of dispatching
([MCP transport](MCP-PROTOCOL.md#transport-behavior)).

### Resolution rules

| Route floor | Client-requested | Effective isolation |
|-------------|-----------------|---------------------|
| `repeatable read` | none | `repeatable read` |
| `repeatable read` | `read committed` | `repeatable read` (floor wins) |
| `repeatable read` | `serializable` | `serializable` (escalation) |
| none | `serializable` | `serializable` |
| none | none | connection default |

**`isolation = max(route floor, client-requested)`** — a client may escalate,
never downgrade. A downgrade below the floor is not rejected at runtime; it is
structurally unrepresentable.

The `X-PGMI-Transaction-Isolation` header is therefore escalation, not
obligation. Routes just work for callers that send nothing.

Source: `lib/api/00-transaction-isolation.sql` — `internal.resolve_transaction_policy`.

## DEFERRABLE derivation

When the resolved characteristics are `SERIALIZABLE READ ONLY`, the resolver
sets `transaction_deferrable = true`. A `DEFERRABLE` transaction waits for a
conflict-free snapshot at `BEGIN` and can then never abort with `40001` — those
routes need no retry logic at all.

Any other combination yields `transaction_deferrable = false`.

Source: `lib/api/00-transaction-isolation.sql` — `internal.resolve_transaction_policy`, lines 129–130.

## The fail-closed invariant

`SET TRANSACTION` is transaction control and is illegal inside functions. The
SQL gateways can only **read** the current level, never set it — so they read
`current_setting('transaction_isolation')` and
`current_setting('transaction_read_only')` and reject a shortfall before
dispatching:

| Protocol | Response |
|----------|----------|
| REST | `428 Precondition Required` |
| RPC | HTTP 428 with JSON-RPC error |
| MCP | `-32603` envelope, HTTP 200 |

The error carries `pgmi.transaction_isolation_too_weak` or
`pgmi.transaction_read_only_required`.

This is what a proxy that skips the policy lookup gets. It is no longer the
path correct callers travel — it is the safety net.

In a `READ ONLY` transaction, the gateways also skip their own writes:
exchange auto-logging is suppressed, and a login session's idle expiry is
not extended.

Source: `lib/api/09-gateways.sql` — `api.rest_invoke`, `api.rpc_invoke`, and `internal.mcp_dispatch` behind `api.mcp_call_tool`, `api.mcp_read_resource` and `api.mcp_get_prompt`.

## Replica routing

OpenAPI advertises `x-pgmi-read-only` and the derived `x-pgmi-replica-safe` on
each operation. `readOnly` alone is **not** sufficient to offload to a hot
standby, because a standby caps at `repeatable read` — `SERIALIZABLE` is not
supported there. So:

| Route | `x-pgmi-replica-safe` |
|-------|-----------------------|
| `readOnly: true`, floor ≤ `repeatable read` | `true` |
| `readOnly: true`, floor = `serializable` | `false` (primary only) |
| `readOnly: false` (any floor) | `false` |

pgmi does not route; the deployment's fronting gateway consumes the hint.

Source: `lib/api/00-transaction-isolation.sql` — `internal.transaction_policy_replica_safe`.

## Serialization-failure retry contract

Declaring an isolation floor buys a stronger guarantee at the price of
transient aborts. Under `repeatable read` / `serializable`, PostgreSQL aborts
conflicting transactions with `40001` (`serialization_failure`); `40P01`
(`deadlock_detected`) can occur at any level.

### What the gateways do

The gateways **propagate `40001` and `40P01` with SQLSTATE intact** instead of
sanitizing them into a generic 500. Flattened into a 500, a client cannot
distinguish "your transaction lost a race, retry it" from "this handler is
broken." Every other SQLSTATE keeps the sanitizing behavior — `SQLERRM` /
`DETAIL` never reach a client.

### Why catching them in PL/pgSQL is unsafe

Catching a serialization failure is not merely unhelpful — it is **unsafe**.
The failed statement rolls back to the exception block's implicit savepoint,
but the transaction stays alive and **commits**. The handler's write silently
vanishes while the client is told "internal error." Verified with a live
two-transaction conflict test
(`internal/scaffold/serialization_retry_integration_test.go`).

A savepoint cannot refresh the snapshot, which is frozen for the transaction's
life under `repeatable read` / `serializable` — so an in-transaction retry
re-reads identical data and conflicts forever. Only `ROLLBACK` + a fresh
`BEGIN` converges.

### Who retries

**Retry belongs to whoever owns `BEGIN` — the client.** Every reference
gateway in `gateways/` retries up to `RETRY_MAX_ATTEMPTS` (default 3) with
exponential backoff + jitter, opening a **new transaction per attempt**. The
retry is a decorator on the gateway's database channel
(`gateways/PROTOCOL.md`, "Extension seams"). On exhaustion it answers with the machine token
`pgmi.transaction_retryable` and a `Retry-After` header: REST as a `409`
problem, MCP as the JSON-RPC error `-31000` (HTTP 200) whose
`data.code` carries the token, so the agent repeats the call unchanged.

An operator-supplied REST/RPC proxy must implement the same loop.

### Idempotency requirement

Handlers on retryable routes must be idempotent. A retry re-runs the entire
handler, so any side effect outside the transaction (outbound HTTP, queue
publish, non-idempotent external write) happens again.

## Comparison with PostgREST

[PostgREST v13](https://docs.postgrest.org/en/v13/references/transactions.html)
sets isolation at the role level (`ALTER ROLE ... SET
default_transaction_isolation`) or the function level (`CREATE FUNCTION ... SET
default_transaction_isolation`) — static server-side configuration.

| Capability | pgmi advanced template | PostgREST v13 |
|------------|----------------------|---------------|
| Per-route isolation floor | Yes — metadata key, resolved before `BEGIN` | No — role or function level |
| Client-negotiated escalation | Yes — `X-PGMI-Transaction-Isolation` | No |
| Per-route read-only declaration | Yes — `readOnly: true` | Automatic by HTTP method |
| `DEFERRABLE` derivation | Yes — for `SERIALIZABLE READ ONLY` | No |
| Replica-safe hint in OpenAPI | Yes — `x-pgmi-replica-safe` | No |
| Fail-closed DB-side check | Yes — 428 (REST, RPC `-32600`) / MCP `-32603` | No |

## Limits

- The policy resolver runs on a separate autocommit connection before the
  dispatch transaction opens. That is one extra round trip per request.
- `DEFERRABLE` waits for a conflict-free snapshot — latency at `BEGIN` trades
  off against zero-retry certainty. For short-lived reads this may be worse
  than accepting the occasional `40001`.
- The fail-closed check is a read of two GUCs, not a provable proof that the
  transaction was opened correctly — a compromised client could lie about the
  level if it controls the `SET TRANSACTION` call.

## See also

- [Advanced template overview](README.md#declarative-per-route-transaction-policy)
- [MCP gateway — transaction policy](MCP-GATEWAY.md#transaction-policy)
- [Highlights §9](../HIGHLIGHTS.md#9-routes-declare-their-transaction-policy-and-the-policy-is-resolved-before-begin)
- [`lib/api/00-transaction-isolation.sql`](https://github.com/vvka-141/pgmi/blob/main/shared/templates/advanced/lib/api/00-transaction-isolation.sql)
