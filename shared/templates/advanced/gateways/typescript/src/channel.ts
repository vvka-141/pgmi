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

// The request ran past its deadline: the database cancelled the statement
// (57014 from statement_timeout), the gateway's own timer fired, or no time
// was left to start a statement. 504, never retried.
export class DeadlineExceeded extends Error {
  constructor() {
    super("the request deadline passed");
  }
}

export class RetriesExhausted extends Error {
  constructor(readonly sqlstate: string, readonly attempts: number) {
    super(`retries exhausted: ${sqlstate}`);
  }
}

// -- the real channel -----------------------------------------------------------

// ADAPT: the name operators see in pg_stat_activity and the server log.
export const APPLICATION_NAME = "pgmi-gateway-typescript";
const POOL_TIMEOUT_MS = 5_000;

// DATABASE_URL as libpq reads it (uselibpqcompat): node-postgres otherwise
// treats sslmode=require as verify-full. sslrootcert is the only trust anchor:
// a verifying mode without one is refused, never left to Node's CA store, where
// any public CA could vouch for the database; sslrootcert=system (that store)
// only with verify-full. PGSSLMODE counts as libpq's does; pg reads no
// PGSSLROOTCERT, so only the URL names a root. node-postgres ignores libpq
// parameters it does not know (channel_binding, require_auth), so a URL that
// demands them would connect without them: only the ones it honours pass.
//
// The result is the connection strings to try, in libpq's order. No sslmode
// means prefer: TLS first, plain text when the server has no TLS. allow is
// plain text first, TLS (require, never Node's CA store) when the server
// refuses it. node-postgres has no such fallback, so acquire makes the choice
// on the first connection: the gateway starts, and answers 503, while the
// database is down.
const SSL_MODES = new Set(["disable", "allow", "prefer", "require", "verify-ca", "verify-full"]);
const URL_PARAMETERS = new Set(["sslmode", "sslrootcert", "sslcert", "sslkey", "options", "uselibpqcompat"]);

export function connectionStrings(databaseUrl: string, env: NodeJS.ProcessEnv = process.env): string[] {
  let url: URL;
  try {
    url = new URL(databaseUrl);
  } catch {
    // URL's error quotes its input, password included.
    throw new Error("DATABASE_URL is not a valid connection string");
  }
  for (const key of url.searchParams.keys()) {
    if (!URL_PARAMETERS.has(key)) throw new Error(`DATABASE_URL parameter ${key} is not supported by this gateway`);
  }
  const root = url.searchParams.get("sslrootcert");
  const system = root === "system";
  const mode = url.searchParams.get("sslmode") ?? env.PGSSLMODE ?? (system ? "verify-full" : "prefer");
  if (!SSL_MODES.has(mode)) throw new Error(`sslmode=${mode} is not a libpq sslmode`);
  if (system && mode !== "verify-full") {
    throw new Error(`sslrootcert=system needs sslmode=verify-full, not ${mode}`);
  }
  if (!root && (mode === "verify-ca" || mode === "verify-full")) {
    throw new Error(`sslmode=${mode} needs sslrootcert in DATABASE_URL: the CA file to trust, or system`);
  }
  if (system) url.searchParams.delete("sslrootcert");
  const as = (sslmode: string, forceLibpq = false): string => {
    const attempt = new URL(url);
    attempt.searchParams.set("sslmode", sslmode);
    if (forceLibpq || !attempt.searchParams.has("uselibpqcompat")) attempt.searchParams.set("uselibpqcompat", "true");
    return attempt.toString();
  };
  switch (mode) {
    case "prefer": return [as("prefer", true), as("disable")];
    case "allow": return [as("disable"), as("require", true)];
    default: return [as(mode)];
  }
}

// The server answered the TLS question the other way: no TLS for prefer, or
// a pg_hba.conf that needs encryption for allow.
function tlsMismatch(err: unknown): boolean {
  if (err instanceof pg.DatabaseError) return err.code === "28000" && err.message.includes("no encryption");
  return err instanceof Error && err.message === "The server does not support SSL connections";
}

// Per pool: the Client class that connects with the chosen attempt, and the
// step to the next attempt after a TLS mismatch.
const choices = new WeakMap<pg.Pool, { Client: typeof pg.Client; fallBack: (err: unknown) => boolean }>();

// ADAPT: max is bounded; the server's max_connections is shared by every process.
export function createPool(databaseUrl: string, max = 10): pg.Pool {
  const attempts = connectionStrings(databaseUrl);
  let chosen = 0;
  let decided = attempts.length === 1;
  class Client extends pg.Client {
    constructor(config?: pg.ClientConfig) {
      super({ ...config, connectionString: attempts[chosen] });
    }
  }
  const pool = new pg.Pool({
    connectionString: attempts[0], application_name: APPLICATION_NAME, max, Client,
    connectionTimeoutMillis: POOL_TIMEOUT_MS, idleTimeoutMillis: 30_000,
  });
  pool.on("connect", () => { decided = true; });
  choices.set(pool, {
    Client,
    fallBack: (err) => {
      if (decided || chosen + 1 >= attempts.length || !tlsMismatch(err)) return false;
      chosen++;
      return true;
    },
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
  for (;;) {
    try {
      return await pool.connect();
    } catch (err) {
      if (choices.get(pool)?.fallBack(err)) continue;
      if (err instanceof pg.DatabaseError) throw err;
      throw new PoolTimeout(err instanceof Error ? err.message : String(err), { cause: err });
    }
  }
}

// pg_cancel_backend on a connection of its own, outside the pool: a pool
// whose connections all wait on hung-up requests would never hand one out.
// The gateway login may cancel its own backends. A failure is logged and the
// statement runs on to its statement_timeout.
export async function cancelBackend(pool: pg.Pool, pid: number, requestId: string): Promise<void> {
  const client = new (choices.get(pool)?.Client ?? pg.Client)(pool.options);
  try {
    await client.connect();
    await client.query("SELECT pg_cancel_backend($1)", [pid]);
  } catch (err) {
    console.error(`[gateway] ${requestId} cancel of backend ${pid} failed:`, err instanceof Error ? err.message : err);
  } finally {
    await client.end().catch(() => {});
  }
}

// What the request has left, in milliseconds rounded up, as statement_timeout
// takes it: the server's timer then fires no earlier than the gateway's
// deadline. Never "0": that would disable the timeout.
export function statementTimeout(ctx: RequestContext, now = Date.now()): string {
  const left = ctx.deadline - now;
  if (left <= 0) throw new DeadlineExceeded();
  return String(Math.ceil(left));
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
  // The request deadline is the transaction's statement_timeout, local to it,
  // so the server cancels a statement nobody waits for any more and the
  // pooled connection keeps the server's default. COMMIT can fail with 40001
  // too, so it is inside the call and serializationRetry sees it either way.
  //
  // A client that hangs up aborts ctx.signal, and the abort cancels the
  // running statement on the server: without it the statement runs to its end
  // on a pooled connection nobody waits for. A connection a cancel was sent to
  // is discarded, never pooled: a cancel that lands late would hit the next
  // request's statement.
  async function transaction<Row extends pg.QueryResultRow>(ctx: RequestContext, begin: string, sql: string,
                                                            params: unknown[]): Promise<Row | undefined> {
    ctx.signal.throwIfAborted();
    const client = await acquire(pool);
    let cancelled: Promise<void> | null = null;
    let onAbort: (() => void) | null = null;
    let rolledBack = true;
    try {
      await client.query(begin);
      const { rows: [{ pid }] } = await client.query<{ pid: number }>(
        "SELECT pg_backend_pid() AS pid, set_config('statement_timeout', $1, true)", [statementTimeout(ctx)]);
      ctx.signal.throwIfAborted();
      onAbort = () => { cancelled = cancelBackend(pool, pid, ctx.requestId); };
      ctx.signal.addEventListener("abort", onAbort, { once: true });
      const { rows } = await client.query<Row>(sql, params);
      await client.query("COMMIT");
      return rows[0];
    } catch (err) {
      rolledBack = await client.query("ROLLBACK").then(() => true, () => false);
      if (sqlstate(err) === "57014" && Date.now() >= ctx.deadline) throw new DeadlineExceeded();
      throw err;
    } finally {
      if (onAbort) ctx.signal.removeEventListener("abort", onAbort);
      if (cancelled) await cancelled;
      client.release(!rolledBack || cancelled !== null);
    }
  }

  const policyRow = (ctx: RequestContext, sql: string, params: unknown[]) => transaction<PolicyRow>(ctx, "BEGIN", sql, params);
  const dispatch = <Row extends pg.QueryResultRow>(ctx: RequestContext, policy: Policy, sql: string, params: unknown[]) =>
    transaction<Row>(ctx, beginStatement(policy), sql, params);

  return {
    async restPolicy(ctx, requested) {
      return toPolicy(await policyRow(ctx,
        "SELECT transaction_isolation, transaction_read_only, transaction_deferrable, handler_name, input_annotations" +
        " FROM api.rest_route_policy($1, $2, $3)", [ctx.method, ctx.url, requested]));
    },

    async restInvoke(ctx, policy, headers, body) {
      const row = await dispatch<{ status_code: number; headers: Record<string, string> | null; content: Buffer | null }>(ctx, policy,
        "SELECT r.status_code, extensions.hstore_to_json(r.headers) AS headers, r.content" +
        " FROM api.rest_invoke($1, $2, extensions.hstore($3::text[], $4::text[]), $5::bytea) r",
        [ctx.method, ctx.url, [...headers.keys()], [...headers.values()], body]);
      if (row === undefined) throw new Error("api.rest_invoke returned no row");
      const out: Record<string, string> = {};
      for (const [name, value] of Object.entries(row.headers ?? {})) out[name.toLowerCase()] = value;
      return { status: row.status_code, headers: out, content: row.content ?? Buffer.alloc(0) };
    },

    async mcpPolicy(ctx, request, requested, headers) {
      const policy = toPolicy(await policyRow(ctx,
        "SELECT transaction_isolation, transaction_read_only, transaction_deferrable, handler_name, input_annotations," +
        " modern, refusal FROM api.mcp_request_policy($1, $2, $3, $4, $5)",
        [JSON.stringify(request), requested, ...headers]));
      if (policy === NO_ROUTE) throw new Error("api.mcp_request_policy returned no row");
      return policy;
    },

    async mcpRequest(ctx, policy, request, context) {
      const row = await dispatch<{ envelope: JsonObject | null }>(ctx, policy,
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
    const path = ctx.url.split("?", 1)[0]; // a query can carry a token
    const line = (what: string) =>
      log(`[trace] ${ctx.requestId} ${op} ${ctx.method} ${path} ${what} ${(performance.now() - start).toFixed(1)}ms`);
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
