// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project: it is a baseline to learn from,
// not a library. The rules it keeps are in ../../PROTOCOL.md ("Extension seams").
//
// The database channel: every call the gateway makes to PostgreSQL for a
// request, behind one interface.
//
//   Channel               one method per call: the policy and the dispatch of
//                         each protocol the gateway serves
//   postgresChannel       the real thing, over a node-postgres Pool
//   tracing               a log line per call: op, route, outcome, time
//   serializationRetry    40001/40P01: the whole transaction again
//   connectionRetry       a dropped connection: again, idempotent calls only
//
// A decorator is a function from Channel to Channel that overrides only the
// calls it changes, so cross-cutting behaviour (caching, metrics, a test
// double) is added in one place, at startup, and no call site changes:
//
//   connectionRetry(serializationRetry(tracing(postgresChannel(pool))))
//
// Both retries sit outside the tracing, so every attempt is a log line of its
// own. Channels hold no per-request state: it travels in the arguments.

import { setTimeout as sleep } from "node:timers/promises";
import pg from "pg";

export type Json = null | boolean | number | string | Json[] | { [key: string]: Json };
export type JsonObject = { [key: string]: Json };

// The x-* keywords of each top-level inputSchema property: {"text": {"x-embed": true}}.
export type Annotations = Readonly<Record<string, Readonly<Record<string, Json>>>>;

// What every channel call and every interceptor may read about the request.
// Read-only: a change is a new object ({...ctx, url}). There is no principal:
// the gateway forwards handles and the database resolves who is calling inside
// the transaction, so nothing before the dispatch knows.
export interface RequestContext {
  readonly requestId: string;
  readonly protocol: "rest" | "mcp";
  readonly method: string;        // REST: the HTTP method; MCP: the JSON-RPC method
  readonly url: string;           // REST: the URL under /api; MCP: /mcp
  readonly deadline: number;      // Date.now() after which the request is abandoned
  readonly signal: AbortSignal;   // aborts at the deadline or when the client hangs up
  readonly handler: string | null;
  readonly annotations: Annotations;
}

export type Isolation = "read committed" | "repeatable read" | "serializable";

// One row of api.rest_route_policy or api.mcp_request_policy. isolation null:
// the connection default.
export interface Policy {
  readonly isolation: Isolation | null;
  readonly readOnly: boolean;
  readonly deferrable: boolean;
  readonly handler: string | null;
  readonly annotations: Annotations | null;
  readonly modern: boolean;              // MCP only: the 2026-07-28 era
  readonly refusal: JsonObject | null;   // MCP only: the answer instead of a dispatch
}

export const NO_ROUTE: Policy = {
  isolation: null, readOnly: false, deferrable: false, handler: null, annotations: null, modern: false, refusal: null,
};

export interface RestResult {
  readonly status: number;
  readonly headers: Readonly<Record<string, string>>;   // lower-case names
  readonly content: Buffer;
}

export type McpHeaders = readonly [version: string | null, method: string | null, name: string | null];

// The MCP context api.mcp_handle_request resolves: a verified OAuth client or
// a connection handle. Never a token, never a user name.
export type McpContext =
  | { readonly client: { readonly issuer: string; readonly subject: string; readonly clientId: string } }
  | { readonly connection: string };

// ADAPT: a gateway that serves RPC adds rpcPolicy/rpcInvoke here, and every
// decorator gets them too.
export interface Channel {
  restPolicy(ctx: RequestContext, requested: Isolation | null): Promise<Policy>;
  restInvoke(ctx: RequestContext, policy: Policy, headers: ReadonlyMap<string, string>, body: Buffer | null): Promise<RestResult>;
  mcpPolicy(ctx: RequestContext, request: Json, requested: Isolation | null, headers: McpHeaders): Promise<Policy>;
  mcpRequest(ctx: RequestContext, policy: Policy, request: Json, context: McpContext | null): Promise<JsonObject | null>;
}

// -- errors the gateway maps to answers (gateway.ts, answerError) ------------

// No pooled connection within the acquire timeout: 503, never retried here.
export class PoolTimeout extends Error {}

export class RetriesExhausted extends Error {
  constructor(readonly sqlstate: string, readonly attempts: number) {
    super(`retries exhausted: ${sqlstate}`);
  }
}

// -- the real channel -----------------------------------------------------------

// ADAPT: the name operators see in pg_stat_activity and the server log.
export const APPLICATION_NAME = "pgmi-gateway-typescript";
const POOL_TIMEOUT_MS = 5_000;

// ADAPT: bounded; the server's max_connections is shared by every process.
// sslmode in DATABASE_URL is read as libpq reads it (uselibpqcompat):
// node-postgres otherwise treats require as verify-full, which is not what
// the URL says.
export function createPool(connectionString: string, max = 10): pg.Pool {
  const url = new URL(connectionString);
  if (url.searchParams.has("sslmode") && !url.searchParams.has("uselibpqcompat")) url.searchParams.set("uselibpqcompat", "true");
  const pool = new pg.Pool({
    connectionString: url.toString(), application_name: APPLICATION_NAME, max,
    connectionTimeoutMillis: POOL_TIMEOUT_MS, idleTimeoutMillis: 30_000,
  });
  // An idle connection the server closes emits here; without a listener it
  // would end the process.
  pool.on("error", (err) => console.error("[gateway] idle connection lost:", err.message));
  return pool;
}


const ISOLATION_SQL: Record<Isolation, string> = {
  "read committed": "ISOLATION LEVEL READ COMMITTED",
  "repeatable read": "ISOLATION LEVEL REPEATABLE READ",
  "serializable": "ISOLATION LEVEL SERIALIZABLE",
};

// PostgreSQL folds "read uncommitted" onto "read committed"; so do we.
const CANONICAL_ISOLATION: Record<string, Isolation> = {
  "read committed": "read committed",
  "read uncommitted": "read committed",
  "repeatable read": "repeatable read",
  "serializable": "serializable",
};

// X-PGMI-Transaction-Isolation: case and separators free; null when unknown.
export function normalizeIsolation(value: string): Isolation | null {
  return CANONICAL_ISOLATION[value.trim().toLowerCase().replace(/[\s_-]+/g, " ")] ?? null;
}

// BEGIN with the route's characteristics, built from fixed text only, so a
// pooled connection carries nothing from one request to the next.
export function beginStatement(policy: Policy): string {
  const modes = policy.isolation ? [ISOLATION_SQL[policy.isolation]] : [];
  modes.push(policy.readOnly ? "READ ONLY" : "READ WRITE");
  if (policy.deferrable) modes.push("DEFERRABLE");
  return "BEGIN " + modes.join(" ");
}

// pg's Pool rejects with a plain Error when no connection comes within
// connectionTimeoutMillis, and a server that cannot be reached is the same
// for the caller: nothing was sent. Only a server answer carries a SQLSTATE.
export async function acquire(pool: pg.Pool): Promise<pg.PoolClient> {
  try {
    return await pool.connect();
  } catch (err) {
    if (err instanceof pg.DatabaseError) throw err;
    throw new PoolTimeout(err instanceof Error ? err.message : String(err), { cause: err });
  }
}

interface PolicyRow {
  transaction_isolation: string | null;
  transaction_read_only: boolean;
  transaction_deferrable: boolean;
  handler_name: string | null;
  input_annotations: Annotations | null;
  modern?: boolean;
  refusal?: JsonObject | null;
}

function toPolicy(row: PolicyRow | undefined): Policy {
  if (row === undefined) return NO_ROUTE;
  return {
    isolation: row.transaction_isolation === null ? null : normalizeIsolation(row.transaction_isolation),
    readOnly: row.transaction_read_only,
    deferrable: row.transaction_deferrable,
    handler: row.handler_name,
    annotations: row.input_annotations,
    modern: row.modern ?? false,
    refusal: row.refusal ?? null,
  };
}

// Each policy call runs on a pooled connection in its own short transaction;
// each dispatch in one transaction opened with the resolved characteristics.
// The policy costs one round trip per request. It changes only at deploy
// time: a caching decorator would key it on api.catalog_version().
export function postgresChannel(pool: pg.Pool): Channel {
  async function policyRow(sql: string, params: unknown[]): Promise<PolicyRow | undefined> {
    const client = await acquire(pool);
    try {
      const { rows } = await client.query<PolicyRow>(sql, params);
      client.release();
      return rows[0];
    } catch (err) {
      client.release(err instanceof Error ? err : true);
      throw err;
    }
  }

  // COMMIT can fail with 40001 too, so it is inside the call and
  // serializationRetry sees it either way.
  async function dispatch<Row extends pg.QueryResultRow>(policy: Policy, sql: string, params: unknown[]): Promise<Row | undefined> {
    const client = await acquire(pool);
    try {
      await client.query(beginStatement(policy));
      const { rows } = await client.query<Row>(sql, params);
      await client.query("COMMIT");
      client.release();
      return rows[0];
    } catch (err) {
      const rolledBack = await client.query("ROLLBACK").then(() => true, () => false);
      client.release(rolledBack ? undefined : true);
      throw err;
    }
  }

  return {
    async restPolicy(ctx, requested) {
      return toPolicy(await policyRow(
        "SELECT transaction_isolation, transaction_read_only, transaction_deferrable, handler_name, input_annotations" +
        " FROM api.rest_route_policy($1, $2, $3)", [ctx.method, ctx.url, requested]));
    },

    async restInvoke(ctx, policy, headers, body) {
      const row = await dispatch<{ status_code: number; headers: Record<string, string> | null; content: Buffer | null }>(policy,
        "SELECT r.status_code, extensions.hstore_to_json(r.headers) AS headers, r.content" +
        " FROM api.rest_invoke($1, $2, extensions.hstore($3::text[], $4::text[]), $5::bytea) r",
        [ctx.method, ctx.url, [...headers.keys()], [...headers.values()], body]);
      if (row === undefined) throw new Error("api.rest_invoke returned no row");
      const out: Record<string, string> = {};
      for (const [name, value] of Object.entries(row.headers ?? {})) out[name.toLowerCase()] = value;
      return { status: row.status_code, headers: out, content: row.content ?? Buffer.alloc(0) };
    },

    async mcpPolicy(ctx, request, requested, headers) {
      const policy = toPolicy(await policyRow(
        "SELECT transaction_isolation, transaction_read_only, transaction_deferrable, handler_name, input_annotations," +
        " modern, refusal FROM api.mcp_request_policy($1, $2, $3, $4, $5)",
        [JSON.stringify(request), requested, ...headers]));
      if (policy === NO_ROUTE) throw new Error("api.mcp_request_policy returned no row");
      return policy;
    },

    async mcpRequest(ctx, policy, request, context) {
      const row = await dispatch<{ envelope: JsonObject | null }>(policy,
        "SELECT (api.mcp_handle_request($1, $2)).envelope",
        [JSON.stringify(request), context === null ? null : JSON.stringify(context)]);
      return row?.envelope ?? null;
    },
  };
}

// -- decorators -------------------------------------------------------------------

type Log = (line: string) => void;

function sqlstate(err: unknown): string | undefined {
  return err instanceof pg.DatabaseError ? err.code : undefined;
}

// One line per call with the request id, so a slow or failing request can be
// followed from the gateway log into the database's exchange log. Never
// bodies or headers: they carry personal data and session handles.
// ADAPT: send it to your logger or OpenTelemetry instead of console.log.
export function tracing(inner: Channel, log: Log = console.log): Channel {
  async function traced<T>(ctx: RequestContext, op: string, call: () => Promise<T>, outcome: (result: T) => string): Promise<T> {
    const start = performance.now();
    const line = (what: string) =>
      log(`[trace] ${ctx.requestId} ${op} ${ctx.method} ${ctx.url} ${what} ${(performance.now() - start).toFixed(1)}ms`);
    try {
      const result = await call();
      line(outcome(result));
      return result;
    } catch (err) {
      line(`error=${sqlstate(err) ?? (err instanceof Error ? err.constructor.name : "unknown")}`);
      throw err;
    }
  }
  return {
    restPolicy: (ctx, requested) =>
      traced(ctx, "rest.policy", () => inner.restPolicy(ctx, requested), (p) => `handler=${p.handler}`),
    restInvoke: (ctx, policy, headers, body) =>
      traced(ctx, "rest.invoke", () => inner.restInvoke(ctx, policy, headers, body), (r) => `status=${r.status}`),
    mcpPolicy: (ctx, request, requested, headers) =>
      traced(ctx, "mcp.policy", () => inner.mcpPolicy(ctx, request, requested, headers),
             (p) => (p.refusal !== null ? "refused" : `handler=${p.handler}`)),
    mcpRequest: (ctx, policy, request, context) =>
      traced(ctx, "mcp.request", () => inner.mcpRequest(ctx, policy, request, context),
             (e) => (e === null ? "notification" : "error" in e ? "error" : "result")),
  };
}

// Exponential backoff with full jitter: unjittered losers of a race retry in
// lockstep and collide again.
function backoff(attempt: number, baseDelayMs: number, maxDelayMs = 1000): number {
  return Math.random() * Math.min(maxDelayMs, baseDelayMs * 2 ** attempt);
}

const RETRYABLE = new Set(["40001", "40P01"]);

// 40001 (serialization failure) and 40P01 (deadlock): the database rolled the
// whole transaction back, so running it again is safe for any call, writes
// included. Each attempt is a new transaction and therefore a fresh snapshot;
// a savepoint or a loop in PL/pgSQL keeps the old snapshot and never
// converges. After the last attempt, RetriesExhausted.
//
// A handler on a route that can hit 40001 must be safe to run twice: a side
// effect outside the transaction (an HTTP call, a queue publish) happens again.
export function serializationRetry(inner: Channel, attempts: number, baseDelayMs: number): Channel {
  async function retrying<T>(ctx: RequestContext, call: () => Promise<T>): Promise<T> {
    for (let attempt = 0; ; attempt++) {
      try {
        return await call();
      } catch (err) {
        const code = sqlstate(err);
        if (code === undefined || !RETRYABLE.has(code)) throw err;
        if (attempt >= attempts - 1) throw new RetriesExhausted(code, attempts);
        await sleep(backoff(attempt, baseDelayMs), undefined, { signal: ctx.signal });
      }
    }
  }
  return {
    ...inner,
    restInvoke: (ctx, policy, headers, body) => retrying(ctx, () => inner.restInvoke(ctx, policy, headers, body)),
    mcpRequest: (ctx, policy, request, context) => retrying(ctx, () => inner.mcpRequest(ctx, policy, request, context)),
  };
}

// A failure of the connection, not an answer from the server: class 08
// (connection exception), 57P01..57P03 (the server shut down), or no SQLSTATE
// at all because the socket broke. 40001 is a server answer, and PoolTimeout
// is no connection at all: it is answered 503 at once, not waited out twice.
export function connectionLost(err: unknown): boolean {
  if (err instanceof PoolTimeout) return false;
  if (err instanceof pg.DatabaseError) {
    return err.code !== undefined && (err.code.startsWith("08") || ["57P01", "57P02", "57P03"].includes(err.code));
  }
  if (!(err instanceof Error)) return false;
  const code = (err as NodeJS.ErrnoException).code;
  return /^E[A-Z]+$/.test(code ?? "") || err.message.startsWith("Connection terminated");
}

// Sends a call again when the connection dropped, ONLY if sending it twice
// cannot apply anything twice: the policy lookups (STABLE functions) and a
// dispatch the database runs READ ONLY (the route declared readOnly, so
// PostgreSQL refuses any write in it). A write is never re-sent: the
// connection can drop after the server committed and before the answer
// arrived, and a second send would apply it twice. MCP is never read-only
// (every exchange is logged), so its dispatch is never retried here.
//
// The HTTP method says nothing: a GET handler can write. The database's
// readOnly declaration does.
export function connectionRetry(inner: Channel, attempts = 2, baseDelayMs = 50, log: Log = console.log): Channel {
  async function retrying<T>(ctx: RequestContext, op: string, call: () => Promise<T>): Promise<T> {
    for (let attempt = 0; ; attempt++) {
      try {
        return await call();
      } catch (err) {
        if (!connectionLost(err) || attempt >= attempts - 1) throw err;
        log(`[retry] ${ctx.requestId} ${op}: connection lost (${(err as Error).message}), attempt ${attempt + 2}`);
        await sleep(backoff(attempt, baseDelayMs), undefined, { signal: ctx.signal });
      }
    }
  }
  return {
    ...inner,
    restPolicy: (ctx, requested) => retrying(ctx, "rest.policy", () => inner.restPolicy(ctx, requested)),
    restInvoke: (ctx, policy, headers, body) => policy.readOnly
      ? retrying(ctx, "rest.invoke", () => inner.restInvoke(ctx, policy, headers, body))
      : inner.restInvoke(ctx, policy, headers, body),
    mcpPolicy: (ctx, request, requested, headers) =>
      retrying(ctx, "mcp.policy", () => inner.mcpPolicy(ctx, request, requested, headers)),
  };
}

// The context with what the policy says about the handler.
export function withPolicy(ctx: RequestContext, policy: Policy): RequestContext {
  return { ...ctx, handler: policy.handler, annotations: policy.annotations ?? {} };
}
