// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library. Namespaces, modules and package names are
// examples: rename them and add the project's specifics freely. The wire
// rules it keeps are in ../../PROTOCOL.md.
//
// The HTTP side of the gateway, on plain node:http:
//
//   ANY  /api/...        REST: api.rest_invoke, one transaction per request
//   POST /mcp            MCP, both protocol eras: api.mcp_handle_request
//   GET  /auth/login, GET /auth/callback, POST /auth/logout   OIDC sign-in
//   POST /auth/dev-login development sign-in, DEV_LOGIN=1 only
//   GET  /.well-known/oauth-protected-resource[/mcp]   with MCP_RESOURCE
//   GET  /health
//
// Shape of a request: route() picks a handler; the handler reads, checks and
// dispatches, and throws a typed error for anything it does not answer
// itself; answerError() turns every error into its answer, in one place, per
// protocol (a problem on REST, a JSON-RPC envelope on MCP).
//
// No web framework: the routing is a dozen lines, and the rules that matter
// (headers seen raw, the body counted, the status chosen) are easier to read
// without one. ADAPT: in an Express, Fastify or Hono project, keep the
// handlers and errors and let the framework route.

import { randomUUID } from "node:crypto";
import type { IncomingMessage, ServerResponse } from "node:http";
import type pg from "pg";
import {
  type Channel, type Isolation, type Json, type JsonObject, type McpContext, type McpHeaders, normalizeIsolation,
  PoolTimeout, type RequestContext, RetriesExhausted, withPolicy,
} from "./channel.js";
import {
  InterceptorFailed, isObject, type Pipeline, RequestMessage, ResponseMessage, runInterceptors, selected,
} from "./interceptors.js";
import {
  clearCookie, CSRF_COOKIE, CSRF_HEADER, forwardedHeaders, HeaderCollision, LOGIN_COOKIE, LoginFailed, LoginTransport,
  readCookies, SESSION_COOKIE,
} from "./login.js";
import { identityContext, ProviderUnavailable, RateLimiter, ResourceServer, TokenRejected } from "./resource-server.js";

export const MAX_BODY_BYTES = 1024 * 1024;
// A request is abandoned after this; interceptors read what is left of it.
// ADAPT: shorter for an API that never waits on another service.
export const REQUEST_DEADLINE_MS = 30_000;
const REST_PREFIX = "/api";
const RETRY_AFTER_SECONDS = "1";
const NOT_COPIED = new Set(["content-length", "transfer-encoding", "connection"]);

// -- the errors a handler throws; answerError() maps each one -----------------

// An RFC 9457 problem the gateway answers itself.
export class Problem extends Error {
  constructor(readonly status: number, readonly title: string, readonly code: string,
              readonly detail?: string, readonly headers: Record<string, string> = {}) {
    super(code);
  }
}

// 401 with the challenge that tells an MCP client where the
// protected-resource metadata is (RFC 9728 §5.1), when OAuth is on. No body.
export class Unauthorized extends Error {
  constructor(readonly challenge: string) {
    super("unauthorized");
  }
}

// A JSON-RPC error the MCP endpoint answers before dispatching.
export class RpcError extends Error {
  constructor(readonly status: number, readonly rpcCode: number, message: string, readonly data?: JsonObject) {
    super(message);
  }
}

const notFound = () => new Problem(404, "Not Found", "pgmi.not_found");

export interface GatewayOptions {
  pool: pg.Pool;                       // sign-in calls; everything per request goes through channel
  channel: Channel;
  pipeline: Pipeline;
  login?: LoginTransport;              // undefined: no sign-in, /api is anonymous
  resource?: ResourceServer;           // undefined: /mcp takes connection handles only
  perIp: RateLimiter;
  perClient: RateLimiter;
  allowedOrigins: ReadonlySet<string>;
  log?: (line: string) => void;
}

// One request in flight: what every handler and answerError() read.
interface Exchange {
  readonly req: IncomingMessage;
  readonly res: ServerResponse;
  readonly path: string;
  readonly query: string;              // with its "?", or ""
  readonly signal: AbortSignal;
  protocol: "rest" | "mcp" | "auth";
  rpcId: string | number | null;       // MCP: the JSON-RPC id, once parsed
  mcpVersion?: string;                 // MCP: echoed on every JSON answer
  requestId: string;
}

function header(ex: Exchange, name: string): string | undefined {
  const value = ex.req.headers[name];
  return Array.isArray(value) ? value[0] : value;
}

// The q parameter of one media range, defaulting to 1 (RFC 9110 §12.4.2).
// A range with q=0 is an explicit refusal, not an acceptance.
export function acceptsJson(accept: string): boolean {
  return accept.split(",").some((entry) => {
    const [range, ...params] = entry.split(";");
    if (!["application/json", "application/*", "*/*"].includes(range.trim().toLowerCase())) return false;
    const q = params.map((p) => p.split("=")).find(([name]) => name.trim() === "q")?.[1];
    const value = q === undefined ? 1 : Number(q.trim());
    return Number.isNaN(value) || value > 0;
  });
}

// The HTTP status of a dispatched MCP answer (PROTOCOL.md, "MCP status
// codes"), the one piece of protocol knowledge kept here. In the 2026-07-28
// era -32601 is 404 and -32022 is 400; everything else is 200, a tool's
// -32602 included. Every legacy answer is 200.
export function envelopeStatus(envelope: JsonObject, modern: boolean): number {
  const error = envelope.error;
  if (!modern || !isObject(error)) return 200;
  return error.code === -32601 ? 404 : error.code === -32022 ? 400 : 200;
}

// A connection handle sent as Authorization: Bearer cn_..., or undefined.
// A cn_ handle is not an OAuth token and never reaches the verifier.
export function bearerHandle(authorization: string | undefined): string | undefined {
  const [scheme, ...rest] = (authorization ?? "").split(" ");
  const credentials = rest.join(" ").trim();
  return scheme.toLowerCase() === "bearer" && credentials.startsWith("cn_") ? credentials : undefined;
}

// The database knows no live connection by the handle sent: answered 401,
// as a token the gateway refused.
export function handleUnknown(envelope: JsonObject): boolean {
  const error = envelope.error;
  return isObject(error) && error.code === -31001 && isObject(error.data) && error.data.code === "pgmi.connection_unknown";
}

export function rpcIdOf(message: Json): string | number | null {
  const id = isObject(message) ? message.id : null;
  return typeof id === "string" || (typeof id === "number" && Number.isInteger(id)) ? id : null;
}

// What api.mcp_request_policy routes on: an interceptor that changes it sends
// the request to another handler, so the policy is resolved again.
export function mcpRoute(request: Json): string {
  const params = isObject(request) && isObject(request.params) ? request.params : {};
  return JSON.stringify([isObject(request) ? request.method ?? null : null, params.name ?? null, params.uri ?? null]);
}

function requestedIsolation(value: string | undefined): Isolation | null {
  if (!value) return null;
  const level = normalizeIsolation(value);
  if (level === null) throw new Problem(400, "Bad Request", "pgmi.transaction_isolation_unsupported");
  return level;
}

export function createGateway(options: GatewayOptions) {
  const { pool, channel, pipeline, login, resource, perIp, perClient, allowedOrigins } = options;
  const log = options.log ?? console.log;

  function context(ex: Exchange, protocol: "rest" | "mcp", method: string, url: string): RequestContext {
    return { requestId: ex.requestId, protocol, method, url, deadline: Date.now() + REQUEST_DEADLINE_MS, signal: ex.signal,
             handler: null, annotations: {} };
  }

  // DNS-rebinding protection: a browser sends Origin; any value not on the
  // allowlist is refused. Non-browser clients send none.
  function checkOrigin(ex: Exchange): void {
    const origin = header(ex, "origin");
    if (origin !== undefined && !allowedOrigins.has(origin)) throw new Problem(403, "Forbidden", "pgmi.origin_refused");
  }

  function checkCsrf(ex: Exchange, loginSession: string | undefined): void {
    if (login && !login.csrfOk(ex.req.method!, loginSession, header(ex, CSRF_HEADER))) {
      throw new Problem(403, "Forbidden", "pgmi.csrf_refused", `send the ${CSRF_COOKIE} value in X-CSRF-Token`);
    }
  }

  function limit(limiter: RateLimiter, key: string): void {
    const [allowed, retryAfter] = limiter.allow(key);
    if (!allowed) {
      throw new Problem(429, "Too Many Requests", "pgmi.rate_limited", undefined,
                        { "Retry-After": String(retryAfter), Connection: "close" });
    }
  }

  // Counted as it arrives: a declared or a streamed body over the limit is 413
  // before the database is called. A client that stops sending is cut off by
  // the server's requestTimeout (main.ts) and by the request signal.
  async function readBody(ex: Exchange): Promise<Buffer> {
    const tooLarge = new Problem(413, "Content Too Large", "pgmi.body_too_large", `the limit is ${MAX_BODY_BYTES} bytes`,
                                 { Connection: "close" });
    if (Number(ex.req.headers["content-length"] ?? 0) > MAX_BODY_BYTES) throw tooLarge;
    const chunks: Buffer[] = [];
    let length = 0;
    const stop = () => ex.req.destroy(ex.signal.reason as Error);
    ex.signal.addEventListener("abort", stop, { once: true });
    try {
      for await (const chunk of ex.req) {
        length += (chunk as Buffer).length;
        if (length > MAX_BODY_BYTES) throw tooLarge;
        chunks.push(chunk as Buffer);
      }
    } finally {
      ex.signal.removeEventListener("abort", stop);
    }
    return Buffer.concat(chunks);
  }

  function sendJson(ex: Exchange, status: number, body: Json, headers: Record<string, string> = {}): void {
    const data = Buffer.from(JSON.stringify(body));
    ex.res.writeHead(status, {
      "Content-Type": "application/json", "Content-Length": String(data.length),
      ...(ex.mcpVersion ? { "MCP-Protocol-Version": ex.mcpVersion } : {}), ...headers,
    });
    ex.res.end(data);
  }

  function sendProblem(ex: Exchange, p: Problem): void {
    const body = Buffer.from(JSON.stringify({ type: "about:blank", title: p.title, status: p.status, code: p.code,
                                              ...(p.detail ? { detail: p.detail } : {}) }));
    ex.res.writeHead(p.status, { "Content-Type": "application/problem+json", "Content-Length": String(body.length),
                                 "Cache-Control": "no-store", ...p.headers });
    ex.res.end(body);
  }

  function sendEmpty(ex: Exchange, status: number, headers: Record<string, string | string[]>): void {
    ex.res.writeHead(status, { "Content-Length": "0", ...headers });
    ex.res.end();
  }

  function rpcError(ex: Exchange, status: number, code: number, message: string, data?: JsonObject,
                    headers: Record<string, string> = {}): void {
    sendJson(ex, status, { jsonrpc: "2.0", id: ex.rpcId, error: { code, message, ...(data ? { data } : {}) } }, headers);
  }

  // Every error a handler throws, answered in one place. The client never
  // sees SQL text or a stack: anything unexpected is logged and answered 500.
  function answerError(ex: Exchange, err: unknown): void {
    if (ex.res.headersSent || ex.res.destroyed) {
      ex.res.destroy();
      return;
    }
    const mcp = ex.protocol === "mcp";
    if (err instanceof Problem) return sendProblem(ex, err);
    if (err instanceof Unauthorized) return sendEmpty(ex, 401, { "WWW-Authenticate": err.challenge, Connection: "close" });
    if (err instanceof RpcError) return rpcError(ex, err.status, err.rpcCode, err.message, err.data);
    if (err instanceof RetriesExhausted) {
      // The transaction lost a race every time it ran: nothing was applied,
      // and the same call may succeed when repeated.
      log(`[gateway] ${ex.requestId} serialization failure, retries exhausted (${err.attempts} attempts): ${err.sqlstate}`);
      if (mcp) {
        return rpcError(ex, 200, -31000, "The call conflicted with a concurrent change and was rolled back; repeat it unchanged",
                        { code: "pgmi.transaction_retryable", sqlstate: err.sqlstate, attempts: err.attempts },
                        { "Retry-After": RETRY_AFTER_SECONDS });
      }
      return sendProblem(ex, new Problem(409, "Conflict", "pgmi.transaction_retryable",
                                         "the request conflicted with a concurrent change; repeat it",
                                         { "Retry-After": RETRY_AFTER_SECONDS }));
    }
    if (err instanceof InterceptorFailed) {
      if (mcp) {
        return rpcError(ex, 502, -32603, "The gateway could not complete the call",
                        { code: "pgmi.interceptor_failed", requestId: ex.requestId });
      }
      // After the commit the request was applied: say so, so the client does
      // not repeat a write that already happened.
      return sendProblem(ex, new Problem(502, "Bad Gateway", "pgmi.interceptor_failed",
        err.applied ? "the request was applied; its answer could not be completed" : "the request was not applied",
        { "X-Request-Id": ex.requestId }));
    }
    if (err instanceof PoolTimeout) {
      log(`[gateway] ${ex.requestId} no pooled connection: ${err.message}`);
      if (mcp) {
        return rpcError(ex, 503, -31000, "The database is unavailable; repeat the call later", { code: "pgmi.unavailable" },
                        { "Retry-After": RETRY_AFTER_SECONDS });
      }
      return sendProblem(ex, new Problem(503, "Service Unavailable", "pgmi.unavailable", undefined,
                                         { "Retry-After": RETRY_AFTER_SECONDS }));
    }
    if (err instanceof ProviderUnavailable) {
      log(`[gateway] ${ex.requestId} identity provider unavailable: ${err.message}`);
      return rpcError(ex, 503, -31000, "The identity provider is unavailable; repeat the call later",
                      { code: "pgmi.identity_provider_unavailable" }, { "Retry-After": "5", Connection: "close" });
    }
    if (err instanceof LoginFailed) {
      log(`[gateway] ${ex.requestId} sign-in refused: ${err.code} (${err.detail})`);
      return sendProblem(ex, new Problem(err.status, "Sign-in failed", err.code));
    }
    if (ex.signal.aborted) {
      // The client hung up or the deadline passed: nobody reads an answer.
      log(`[gateway] ${ex.requestId} abandoned: ${String(ex.signal.reason)}`);
      ex.res.destroy();
      return;
    }
    log(`[gateway] ${ex.requestId} error: ${err instanceof Error ? err.stack ?? err.message : String(err)}`);
    if (mcp) return rpcError(ex, 500, -32603, "Internal error");
    sendProblem(ex, new Problem(500, "Internal Server Error", "pgmi.internal_error"));
  }

  // -- REST ------------------------------------------------------------------------

  // The login session comes from the cookie, never from a client header: the
  // database trusts x-login-session because only this gateway can set it.
  async function rest(ex: Exchange): Promise<void> {
    const method = ex.req.method!;
    checkOrigin(ex);
    const loginSession = login ? readCookies(ex.req.headers.cookie).get(SESSION_COOKIE) : undefined;
    checkCsrf(ex, loginSession);
    let headers = forwarded(ex.req.rawHeaders, loginSession);
    const requested = requestedIsolation(headers.get("x-pgmi-transaction-isolation"));
    const url = (ex.path.slice(REST_PREFIX.length) || "/") + ex.query;
    const read = await readBody(ex);
    let body = read.length ? read : null;

    let ctx = context(ex, "rest", method, url);
    let policy = await channel.restPolicy(ctx, requested);
    ctx = withPolicy(ctx, policy);
    const before = selected(pipeline.requests, ctx);
    if (before.length) {
      // The policy call returned its connection: none is held while an
      // interceptor waits on another service.
      const request = new RequestMessage(method, url, Object.fromEntries(headers), body);
      await runInterceptors(before, request, ctx, false, log);
      // Identity is the gateway's: whatever an interceptor did to the
      // headers, the session is the cookie's again.
      headers = forwarded(Object.entries(request.headers).flat(), loginSession);
      body = request.body;
      if (request.method !== ctx.method || request.url !== ctx.url) {
        ctx = { ...ctx, method: request.method, url: request.url };
        policy = await channel.restPolicy(ctx, requested);
        ctx = withPolicy(ctx, policy);
      }
    }

    const result = await channel.restInvoke(ctx, policy, headers, body);
    let { status, content } = result;
    let responseHeaders: Record<string, string> = { ...result.headers };
    const after = selected(pipeline.responses, ctx);
    if (after.length) {
      const response = new ResponseMessage(status, responseHeaders, content);
      await runInterceptors(after, response, ctx, true, log);
      ({ status, headers: responseHeaders } = response);
      content = response.body ?? Buffer.alloc(0);
    }

    // Every header the database returned, except hop-by-hop ones and the
    // length, which is the length of what is sent. HEAD sends nothing and
    // declares the length GET would have sent (RFC 9110 §8.6).
    const out: Record<string, string> = {};
    for (const [name, value] of Object.entries(responseHeaders)) if (!NOT_COPIED.has(name)) out[name] = value;
    if (method !== "HEAD") out["content-length"] = String(content.length);
    else if (responseHeaders["content-length"] !== undefined) out["content-length"] = responseHeaders["content-length"];
    ex.res.writeHead(status, out);
    ex.res.end(method === "HEAD" ? undefined : content);
  }

  function forwarded(rawHeaders: readonly string[], loginSession: string | undefined): Map<string, string> {
    try {
      return forwardedHeaders(rawHeaders, loginSession);
    } catch (err) {
      if (err instanceof HeaderCollision) {
        throw new Problem(400, "Bad Request", "pgmi.header_collision", `header ${err.message} is sent more than once`);
      }
      throw err;
    }
  }

  // -- MCP ---------------------------------------------------------------------------

  // The MCP context for the database. A connection handle comes as
  // Authorization: Bearer cn_... or, with no other Authorization, as
  // X-Connection, and goes on as it is: the database resolves it. With
  // MCP_RESOURCE set, any other bearer token is verified here and only the
  // identity it proves goes on; a request with neither is 401, so a client
  // learns where to get a token.
  async function mcpCaller(ex: Exchange): Promise<McpContext | null> {
    const authorization = header(ex, "authorization");
    const connection = bearerHandle(authorization)
      ?? (resource === undefined || authorization === undefined ? header(ex, "x-connection") || undefined : undefined);
    if (connection !== undefined || resource === undefined) {
      return connection ? { connection } : null;
    }
    let identity;
    try {
      identity = await resource.verify(authorization);
    } catch (err) {
      if (err instanceof TokenRejected) {
        log(`[gateway] ${ex.requestId} token refused: ${err.message}`);   // the reason, never the token
        throw new Unauthorized(resource.challenge(err.error));
      }
      throw err;
    }
    limit(perClient, JSON.stringify([identity.issuer, identity.subject, identity.clientId]));
    return identityContext(identity);
  }

  // Everything about the protocol is the database's: the body and the three
  // MCP headers go to api.mcp_request_policy as sent, and its refusal is the
  // whole answer. Mcp-Session-Id and Last-Event-ID are ignored: no session.
  async function mcp(ex: Exchange): Promise<void> {
    ex.protocol = "mcp";
    // First, before anything costs: a flood of forged tokens stops here and
    // never reaches the identity provider's key set.
    limit(perIp, ex.req.socket.remoteAddress ?? "");
    checkOrigin(ex);
    if ((header(ex, "content-type") ?? "").split(";")[0].trim().toLowerCase() !== "application/json") throw new Problem(415, "Unsupported Media Type", "pgmi.unsupported_media_type", "MCP requires Content-Type: application/json");
    const accept = header(ex, "accept");
    if (accept && !acceptsJson(accept)) throw new Problem(406, "Not Acceptable", "pgmi.not_acceptable", "this endpoint answers application/json");   // JSON answers only, no SSE stream
    ex.mcpVersion = header(ex, "mcp-protocol-version");

    const caller = await mcpCaller(ex);
    const raw = await readBody(ex);
    let request: Json;
    try {
      request = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(raw)) as Json;
    } catch {
      throw new RpcError(400, -32700, "Parse error");
    }
    ex.rpcId = rpcIdOf(request);
    let requested: Isolation | null;
    try {
      requested = requestedIsolation(header(ex, "x-pgmi-transaction-isolation"));
    } catch {
      throw new RpcError(400, -32600, `Unsupported X-PGMI-Transaction-Isolation: ${header(ex, "x-pgmi-transaction-isolation")}`,
                         { code: "pgmi.transaction_isolation_unsupported" });
    }

    let ctx = context(ex, "mcp", isObject(request) ? String(request.method) : "", "/mcp");
    const mcpHeaders: McpHeaders = [header(ex, "mcp-protocol-version") ?? null, header(ex, "mcp-method") ?? null,
                                    header(ex, "mcp-name") ?? null];
    // One call before the transaction: the protocol checks, the route's
    // transaction policy and the handler's metadata. A refusal is the answer:
    // nothing is dispatched, so nothing is logged.
    let policy = await channel.mcpPolicy(ctx, request, requested, mcpHeaders);
    if (policy.refusal === null) {
      ctx = withPolicy(ctx, policy);
      const before = selected(pipeline.requests, ctx);
      if (before.length) {
        // The request is already parsed: the interceptors share it. On /mcp
        // only the body reaches the database.
        const message = new RequestMessage("POST", "/mcp", {}, null, request);
        const route = mcpRoute(request);
        await runInterceptors(before, message, ctx, false, log);
        request = message.json;
        if (mcpRoute(request) !== route) policy = await channel.mcpPolicy(ctx, request, requested, mcpHeaders);
      }
    }
    if (policy.refusal !== null) return sendJson(ex, 400, policy.refusal);

    const envelope = await channel.mcpRequest(ctx, policy, request, caller);
    if (envelope === null) {
      // A notification: accepted, nothing to answer.
      return sendEmpty(ex, 202, ex.mcpVersion ? { "MCP-Protocol-Version": ex.mcpVersion } : {});
    }
    if (handleUnknown(envelope)) {
      throw new Unauthorized(resource?.challenge("invalid_token") ?? 'Bearer error="invalid_token"');
    }
    let status = envelopeStatus(envelope, policy.modern);
    let headers: Record<string, string> = {};
    let answer: Json = envelope;
    const after = selected(pipeline.responses, ctx);
    if (after.length) {
      const response = new ResponseMessage(status, headers, null, envelope);
      await runInterceptors(after, response, ctx, true, log);
      ({ status, headers } = response);
      answer = response.json;
    }
    sendJson(ex, status, answer, headers);
  }

  // -- sign-in ---------------------------------------------------------------------

  async function signIn(ex: Exchange): Promise<void> {
    if (!login?.provider) throw notFound();
    const { location, cookies } = await login.begin(login.provider, new URLSearchParams(ex.query).get("returnTo"));
    sendEmpty(ex, 302, { Location: location, "Cache-Control": "no-store", "Set-Cookie": cookies });
  }

  async function callback(ex: Exchange): Promise<void> {
    if (!login?.provider) throw notFound();
    const clear = { "Set-Cookie": clearCookie(LOGIN_COOKIE) };
    try {
      const { location, cookies } = await login.complete(login.provider, readCookies(ex.req.headers.cookie),
                                                         new URLSearchParams(ex.query), pool);
      sendEmpty(ex, 303, { Location: location, "Cache-Control": "no-store", "Set-Cookie": cookies });
    } catch (err) {
      if (err instanceof LoginFailed) {
        log(`[gateway] ${ex.requestId} sign-in refused: ${err.code} (${err.detail})`);
        throw new Problem(err.status, "Sign-in failed", err.code, undefined, clear);
      }
      if (err instanceof PoolTimeout) throw err;
      log(`[gateway] ${ex.requestId} sign-in error: ${err instanceof Error ? err.message : String(err)}`);
      throw new Problem(500, "Internal Server Error", "pgmi.internal_error", undefined, clear);
    }
  }

  async function logout(ex: Exchange): Promise<void> {
    if (!login) throw notFound();
    checkOrigin(ex);
    const cookies = readCookies(ex.req.headers.cookie);
    checkCsrf(ex, cookies.get(SESSION_COOKIE));
    sendEmpty(ex, 204, { "Cache-Control": "no-store", "Set-Cookie": await login.logout(cookies, pool) });
  }

  // Development sign-in through api.open_dev_login_session, in place of an
  // identity provider. 404 unless DEV_LOGIN=1; the database has the function
  // only in an env=dev deploy, so both must agree.
  async function devSignIn(ex: Exchange): Promise<void> {
    if (!login?.devLogin) throw notFound();
    checkOrigin(ex);
    let body: Json = null;
    try {
      const raw = await readBody(ex);
      body = JSON.parse(raw.length ? raw.toString("utf8") : "{}") as Json;
    } catch (err) {
      if (err instanceof Problem) throw err;
    }
    const { subject, email, displayName } = isObject(body) ? body : {};
    if (typeof subject !== "string" || typeof email !== "string" ||
        (displayName !== undefined && displayName !== null && typeof displayName !== "string")) {
      throw new Problem(400, "Bad Request", "login.request_invalid", 'send {"subject", "email", "displayName"} as JSON strings');
    }
    const cookies = await login.devSignIn(readCookies(ex.req.headers.cookie), subject, email, displayName ?? null, pool);
    sendEmpty(ex, 204, { "Cache-Control": "no-store", "Set-Cookie": cookies });
  }

  // -- routing -------------------------------------------------------------------------

  async function route(ex: Exchange): Promise<void> {
    const { path } = ex;
    const method = ex.req.method;
    if (path === REST_PREFIX || path.startsWith(REST_PREFIX + "/")) return rest(ex);
    if (path === "/mcp") {
      if (method === "POST") return mcp(ex);
      throw new Problem(405, "Method Not Allowed", "pgmi.method_not_allowed", undefined, { Allow: "POST" });   // no SSE stream to open, no session to end
    }
    ex.protocol = "auth";
    if (method === "GET" && path === "/health") return sendJson(ex, 200, { status: "healthy" });
    if (method === "GET" && resource?.metadataPaths.has(path)) return sendJson(ex, 200, resource.metadata());
    if (method === "GET" && path === "/auth/login") return signIn(ex);
    if (method === "GET" && path === "/auth/callback") return callback(ex);
    if (method === "POST" && path === "/auth/logout") return logout(ex);
    if (method === "POST" && path === "/auth/dev-login") return devSignIn(ex);
    throw notFound();
  }

  return async function handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    // One signal per request: it aborts at the deadline or when the client
    // hangs up, and every wait (body, interceptor, backoff) listens to it.
    const abort = new AbortController();
    const deadline = setTimeout(() => abort.abort(new Error("the request deadline passed")), REQUEST_DEADLINE_MS);
    res.on("close", () => {
      clearTimeout(deadline);
      if (!res.writableFinished) abort.abort(new Error("the client closed the connection"));
    });
    const raw = req.url ?? "/";
    const at = raw.indexOf("?");
    const ex: Exchange = {
      req, res, path: at < 0 ? raw : raw.slice(0, at), query: at < 0 ? "" : raw.slice(at),
      signal: abort.signal,
      protocol: "rest", rpcId: null, requestId: randomUUID().replaceAll("-", ""),
    };
    // The path only: a callback's query carries the authorization code.
    res.on("finish", () => log(`[gateway] "${req.method} ${ex.path}" ${res.statusCode}`));
    try {
      await route(ex);
    } catch (err) {
      answerError(ex, err);
    }
  };
}
