// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own tests that fit this project: it is a baseline to learn
// from, not a library.
//
// Unit tests run anywhere and cover what the end-to-end harness
// (internal/scaffold/testdata/gateways/e2e in the pgmi repository) cannot
// reach: the retry rules, the interceptor pipeline's failure and timeout
// paths, the rate limiter's clock. The database tests need an env=dev deploy
// and DATABASE_URL as a login in the api role; without it they are skipped.

import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { createServer, request, type OutgoingHttpHeaders, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { after, before, describe, test } from "node:test";
import pg from "pg";
import {
  acquire, APPLICATION_NAME, cancelBackend, type Channel, connectionLost, connectionRetry, connectionStrings, createPool, DeadlineExceeded, type McpHeaders, NO_ROUTE, normalizeIsolation,
  type Policy, PoolTimeout, postgresChannel, type RequestContext, RetriesExhausted, serializationRetry, statementTimeout, tracing,
} from "./channel.js";
import { acceptsJson, bearerHandle, createGateway, envelopeStatus, handleUnknown, MAX_BODY_BYTES, mcpRoute, rpcIdOf } from "./gateway.js";
import {
  buildPipeline, EMPTY_PIPELINE, embedFields, fakeEmbedder, headerNames, InterceptorFailed, type RequestInterceptor,
  RequestMessage, runInterceptors,
} from "./interceptors.js";
import {
  forwardedHeaders, HeaderCollision, incomingHeaders, LoginTransport, PendingLogins, Provider, safeReturnTo,
} from "./login.js";
import { canonical, RateLimiter, ResourceServer, TokenRejected } from "./resource-server.js";

const quiet = () => {};

function ctx(overrides: Partial<RequestContext> = {}): RequestContext {
  return { requestId: "r1", protocol: "rest", method: "POST", url: "/x", deadline: Date.now() + 30_000,
           signal: new AbortController().signal, handler: null, annotations: {}, ...overrides };
}

function serverError(code: string): pg.DatabaseError {
  const err = new pg.DatabaseError("boom", 0, "error");
  err.code = code;
  return err;
}

// A channel that fails the first `failures` calls of each kind with `error`.
function flaky(error: unknown, failures: number) {
  const calls = { restPolicy: 0, restInvoke: 0, mcpPolicy: 0, mcpRequest: 0 };
  const fail = <T>(op: keyof typeof calls, value: T) => async (): Promise<T> => {
    if (calls[op]++ < failures) throw error;
    return value;
  };
  const channel: Channel = {
    restPolicy: fail("restPolicy", NO_ROUTE),
    restInvoke: fail("restInvoke", { status: 200, headers: {}, content: Buffer.alloc(0) }),
    mcpPolicy: fail("mcpPolicy", NO_ROUTE),
    mcpRequest: fail("mcpRequest", { result: {} }),
  };
  return { channel, calls };
}

const readOnly: Policy = { ...NO_ROUTE, readOnly: true };

describe("headers and settings", () => {
  test("a verifying sslmode needs a trust anchor", () => {
    const base = "postgresql://gw:pw@db.example.com/app";
    const needs = (mode: string) => `sslmode=${mode} needs sslrootcert in DATABASE_URL: the CA file to trust, or system`;
    assert.throws(() => connectionStrings(`${base}?sslmode=verify-ca`, {}), { message: needs("verify-ca") });
    assert.throws(() => connectionStrings(`${base}?sslmode=verify-full`, {}), { message: needs("verify-full") });
    assert.throws(() => connectionStrings(base, { PGSSLMODE: "verify-full" }), { message: needs("verify-full") });
    assert.throws(() => connectionStrings(`${base}?sslmode=bogus`, {}), { message: /not a libpq sslmode/ });
    assert.throws(() => connectionStrings(base, { PGSSLMODE: "bogus" }), { message: /not a libpq sslmode/ });
    assert.throws(() => connectionStrings(`${base}?sslmode=require&sslrootcert=system`, {}),
      { message: "sslrootcert=system needs sslmode=verify-full, not require" });
    const [systemUrl] = connectionStrings(`${base}?sslmode=verify-full&sslrootcert=system`, {});
    const system = new URL(systemUrl!);
    assert.equal(system.searchParams.get("sslrootcert"), null);
    assert.equal(system.searchParams.get("sslmode"), "verify-full");
    const [fromEnvUrl, ...more] = connectionStrings(`${base}?sslrootcert=%2Fetc%2Fca.pem`, { PGSSLMODE: "verify-ca" });
    assert.equal(more.length, 0);
    const fromEnv = new URL(fromEnvUrl!);
    assert.equal(fromEnv.searchParams.get("sslmode"), "verify-ca");
    assert.equal(fromEnv.searchParams.get("uselibpqcompat"), "true");
    const modes = (urls: string[]) => urls.map((u) => {
      const p = new URL(u).searchParams;
      return `${p.get("sslmode")}${p.get("uselibpqcompat") === "true" ? "+libpq" : ""}`;
    });
    assert.deepEqual(modes(connectionStrings(base, {})), ["prefer+libpq", "disable+libpq"]);
    assert.deepEqual(modes(connectionStrings(base, { PGSSLMODE: "prefer" })), ["prefer+libpq", "disable+libpq"]);
    assert.deepEqual(modes(connectionStrings(`${base}?sslmode=allow`, {})), ["disable+libpq", "require+libpq"]);
    assert.deepEqual(modes(connectionStrings(`${base}?sslmode=allow&uselibpqcompat=false`, {})), ["disable", "require+libpq"]);
    assert.deepEqual(modes(connectionStrings(`${base}?sslmode=prefer&uselibpqcompat=false`, {})), ["prefer+libpq", "disable"]);
    assert.deepEqual(modes(connectionStrings(`${base}?sslmode=disable`, {})), ["disable+libpq"]);
    assert.deepEqual(modes(connectionStrings(`${base}?sslmode=require`, {})), ["require+libpq"]);
    for (const key of ["channel_binding", "require_auth", "application_name", "connect_timeout"]) {
      assert.throws(() => connectionStrings(`${base}?${key}=x`, {}),
        { message: `DATABASE_URL parameter ${key} is not supported by this gateway` });
    }
    for (const url of ["postgresql://gw:Pw0rdLEAK@[::1/db", "not a url Pw0rdLEAK"]) {
      assert.throws(() => connectionStrings(url, {}), (err: Error) =>
        err.message === "DATABASE_URL is not a valid connection string" && !err.message.includes("LEAK"));
    }
  });

  test("the login session comes from the cookie, never from the client", () => {
    const headers = forwardedHeaders(["X-Login-Session", "ls_forged", "Accept", "application/json", "X-Connection", "cn_x"], "ls_real");
    assert.equal(headers.get("x-login-session"), "ls_real");
    assert.equal(headers.has("x-connection"), false);
    assert.equal(headers.get("accept"), "application/json");
  });

  test("a repeated header is joined, Cookie with a semicolon", () => {
    assert.equal(forwardedHeaders(["Accept", "application/json", "accept", "*/*"], undefined).get("accept"), "application/json, */*");
    assert.equal(incomingHeaders(["Cookie", "a=1", "cookie", "b=2"]).get("cookie"), "a=1; b=2");
  });

  test("interceptors see every header the client sent, credentials included", () => {
    const headers = incomingHeaders(["Authorization", "Bearer t", "X-Login-Session", "ls_c", "X-Connection", "cn_c",
                                     "Cookie", "a=1", "X-CSRF-Token", "c"]);
    assert.deepEqual([...headers.keys()], ["authorization", "x-login-session", "x-connection", "cookie", "x-csrf-token"]);
    assert.equal(headers.get("x-login-session"), "ls_c");
  });

  test("a repeat of a single-valued header in any case is refused", () => {
    for (const name of ["Authorization", "Content-Length", "Content-Type", "Host", "Origin",
                        "X-CSRF-Token", "X-Organization-Session", "X-PGMI-Transaction-Isolation"]) {
      assert.throws(() => forwardedHeaders([name, "a", name.toLowerCase(), "b"], undefined), HeaderCollision, name);
    }
  });

  test("isolation names are case and separator free", () => {
    assert.equal(normalizeIsolation("Repeatable_Read"), "repeatable read");
    assert.equal(normalizeIsolation("read-uncommitted"), "read committed");
    assert.equal(normalizeIsolation("snapshot"), null);
  });

  test("a state-changing request with the session needs its CSRF token", () => {
    const login = new LoginTransport(undefined, randomBytes(32), true);
    assert.equal(login.csrfOk("GET", "ls_a", undefined), true);
    assert.equal(login.csrfOk("POST", undefined, undefined), true);
    assert.equal(login.csrfOk("POST", "ls_a", undefined), false);
    assert.equal(login.csrfOk("POST", "ls_a", login.csrfToken("ls_b")), false);
    assert.equal(login.csrfOk("POST", "ls_a", login.csrfToken("ls_a")), true);
  });

  test("returnTo stays on this site", () => {
    for (const hostile of ["//evil.example", "/\\evil", "https://evil.example/", "/a b", "/caf€", "", null]) {
      assert.equal(safeReturnTo(hostile), "/api/docs", String(hostile));
    }
    assert.equal(safeReturnTo("/app?tab=2"), "/app?tab=2");
  });

  test("Accept: a q=0 range refuses the type", () => {
    assert.equal(acceptsJson("application/json"), true);
    assert.equal(acceptsJson("text/html, */*;q=0.1"), true);
    assert.equal(acceptsJson("application/json;q=0"), false);
    assert.equal(acceptsJson("application/json;q=0, */*"), false);
    assert.equal(acceptsJson("application/*;q=0, application/json"), true);
    assert.equal(acceptsJson("text/event-stream"), false);
  });

  test("MCP status: era and code", () => {
    assert.equal(envelopeStatus({ error: { code: -32601 } }, true), 404);
    assert.equal(envelopeStatus({ error: { code: -32022 } }, true), 400);
    assert.equal(envelopeStatus({ error: { code: -32602 } }, true), 200);
    assert.equal(envelopeStatus({ error: { code: -32601 } }, false), 200);
    assert.equal(rpcIdOf({ id: 7 }), 7);
    assert.equal(rpcIdOf({ id: true }), null);
    assert.equal(rpcIdOf({ id: 1.5 }), null);
    assert.notEqual(mcpRoute({ method: "tools/call", params: { name: "a" } }), mcpRoute({ method: "tools/call", params: { name: "b" } }));
  });

  test("a connection handle: Bearer cn_... is one, any other bearer is not; an unknown one is 401", () => {
    assert.equal(bearerHandle("Bearer cn_abc"), "cn_abc");
    assert.equal(bearerHandle("bearer  cn_abc "), "cn_abc");
    assert.equal(bearerHandle("Bearer eyJhbGciOi"), undefined);
    assert.equal(bearerHandle(undefined), undefined);
    assert.equal(handleUnknown({ error: { code: -31001, data: { code: "pgmi.connection_unknown" } } }), true);
    assert.equal(handleUnknown({ error: { code: -31001, data: { code: "membership.connection.paused" } } }), false);
  });
});

describe("the database channel", () => {
  test("a dropped connection re-sends a policy lookup and a read-only dispatch", async () => {
    const { channel, calls } = flaky(new Error("Connection terminated unexpectedly"), 1);
    const retrying = connectionRetry(channel, 2, 0, quiet);
    await retrying.restPolicy(ctx(), null);
    await retrying.restInvoke(ctx(), readOnly, new Map(), null);
    await retrying.mcpPolicy(ctx(), {}, null, [null, null, null]);
    assert.deepEqual(calls, { restPolicy: 2, restInvoke: 2, mcpPolicy: 2, mcpRequest: 0 });
  });

  test("a write is never re-sent after a dropped connection: it may have committed", async () => {
    const lost = new Error("Connection terminated unexpectedly");
    const { channel, calls } = flaky(lost, 1);
    const retrying = connectionRetry(channel, 2, 0, quiet);
    await assert.rejects(retrying.restInvoke(ctx(), NO_ROUTE, new Map(), null), lost);
    await assert.rejects(retrying.mcpRequest(ctx(), NO_ROUTE, {}, null), lost);
    assert.equal(calls.restInvoke + calls.mcpRequest, 2);
  });

  test("a pool timeout and a server error are not a dropped connection", () => {
    assert.equal(connectionLost(new PoolTimeout("timeout exceeded when trying to connect")), false);
    assert.equal(connectionLost(serverError("40001")), false);
    assert.equal(connectionLost(serverError("57P01")), true);
    assert.equal(connectionLost(serverError("08006")), true);
    assert.equal(connectionLost(Object.assign(new Error("read ECONNRESET"), { code: "ECONNRESET" })), true);
    assert.equal(connectionLost(new TypeError("a bug")), false);
  });

  test("40001 runs the whole call again, then gives up with the attempts", async () => {
    const { channel, calls } = flaky(serverError("40001"), 5);
    const retrying = serializationRetry(channel, 3, 0);
    await assert.rejects(retrying.mcpRequest(ctx(), NO_ROUTE, {}, null),
                         (err) => err instanceof RetriesExhausted && err.attempts === 3 && err.sqlstate === "40001");
    assert.equal(calls.mcpRequest, 3);
    const once = flaky(serverError("40P01"), 1);
    await serializationRetry(once.channel, 3, 0).restInvoke(ctx(), NO_ROUTE, new Map(), null);
    assert.equal(once.calls.restInvoke, 2);
    const other = flaky(serverError("23505"), 1);
    await assert.rejects(serializationRetry(other.channel, 3, 0).restInvoke(ctx(), NO_ROUTE, new Map(), null));
    assert.equal(other.calls.restInvoke, 1);
  });

  test("statement_timeout is what the request has left, never 0", () => {
    assert.equal(statementTimeout(ctx({ deadline: 10_000 }), 7_500.4), "2499");
    assert.throws(() => statementTimeout(ctx({ deadline: 10_000 }), 9_999.5), DeadlineExceeded);
    assert.throws(() => statementTimeout(ctx({ deadline: 10_000 }), 12_000), DeadlineExceeded);
  });

  test("tracing logs one line per attempt, never a body", async () => {
    const lines: string[] = [];
    const { channel } = flaky(serverError("40001"), 1);
    await serializationRetry(tracing(channel, (l) => lines.push(l)), 3, 0)
      .restInvoke(ctx(), NO_ROUTE, new Map([["x-login-session", "ls_secret"]]), Buffer.from("secret body"));
    assert.equal(lines.length, 2);
    assert.match(lines[0], /^\[trace\] r1 rest\.invoke POST \/x error=40001 /);
    assert.match(lines[1], /status=200/);
    assert.ok(!lines.join("").includes("secret"));
  });
});

describe("interceptors", () => {
  const embedCtx = ctx({ annotations: { text: { "x-embed": true } } });
  const slow = (failure: "fail" | "degrade"): RequestInterceptor => ({
    name: "slow", needsJson: true, failure, timeoutMs: 50, applies: () => true,
    run: async (request) => {
      (request.json as Record<string, unknown>).touched = true;
      await new Promise((resolve) => setTimeout(resolve, 500));
    },
  });

  test("the fake embedder is the one every gateway ships", async () => {
    const [vector] = await fakeEmbedder.embed(["hello"], new AbortController().signal);
    assert.deepEqual(vector, [0.1725, 0.949, 0.302, 0.7294]);
    await assert.rejects(fakeEmbedder.embed(["é".repeat(2001)], new AbortController().signal));
    await fakeEmbedder.embed(["\u{1F600}".repeat(2000)], new AbortController().signal);
  });

  test("embed adds a vector unless the client sent one; other bytes pass as sent", async () => {
    const request = new RequestMessage("POST", "/x", {}, Buffer.from('{"text": "hello", "n": 1}'));
    await runInterceptors([embedFields(fakeEmbedder)], request, embedCtx, false, quiet);
    assert.deepEqual(JSON.parse(request.body!.toString()), { text: "hello", n: 1, text_embedding: [0.1725, 0.949, 0.302, 0.7294] });
    const kept = new RequestMessage("POST", "/x", {}, Buffer.from('{"text": "a", "text_embedding": [1]}'));
    await runInterceptors([embedFields(fakeEmbedder)], kept, embedCtx, false, quiet);
    assert.deepEqual(JSON.parse(kept.body!.toString()).text_embedding, [1]);
    const notJson = new RequestMessage("POST", "/x", {}, Buffer.from("not json"));
    await runInterceptors([embedFields(fakeEmbedder)], notJson, embedCtx, false, quiet);
    assert.equal(notJson.body!.toString(), "not json");
  });

  test("header-names adds the sorted names, never the values, to the arguments", async () => {
    const headers = { "x-probe": "secret", authorization: "Bearer secret", "content-type": "application/json" };
    const rest = new RequestMessage("POST", "/x", headers, Buffer.from('{"text": "a"}'));
    await runInterceptors([headerNames()], rest, ctx(), false, quiet);
    assert.deepEqual(JSON.parse(rest.body!.toString()), { text: "a", headerNames: ["authorization", "content-type", "x-probe"] });
    const call = { jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: "t", arguments: { text: "a" } } };
    const mcp = new RequestMessage("POST", "/mcp", headers, null, call);
    await runInterceptors([headerNames()], mcp, ctx({ protocol: "mcp" }), false, quiet);
    assert.deepEqual(mcp.json, { ...call, params: { ...call.params, arguments: { text: "a", headerNames: ["authorization", "content-type", "x-probe"] } } });
    const none = new RequestMessage("POST", "/mcp", headers, null, { jsonrpc: "2.0", id: 1, method: "ping" });
    await runInterceptors([headerNames()], none, ctx({ protocol: "mcp" }), false, quiet);
    assert.deepEqual(none.json, { jsonrpc: "2.0", id: 1, method: "ping" });
  });

  test("an interceptor that overruns its timeout fails the request", async () => {
    const request = new RequestMessage("POST", "/x", {}, Buffer.from("{}"));
    await assert.rejects(runInterceptors([slow("fail")], request, ctx(), false, quiet),
                         (err) => err instanceof InterceptorFailed && !err.applied);
  });

  test("degrade goes on with the request as it was, none of its changes kept", async () => {
    const request = new RequestMessage("POST", "/x", { a: "1" }, Buffer.from('{"k": 1}'));
    await runInterceptors([slow("degrade")], request, ctx(), false, quiet);
    assert.deepEqual(JSON.parse(request.body!.toString()), { k: 1 });
  });

  test("an unknown INTERCEPTORS name is a startup error", () => {
    assert.throws(() => buildPipeline(["embed", "nope"], "fail"), /unknown interceptor nope/);
    assert.equal(buildPipeline(["embed", "request-id"], "degrade").requests[0].failure, "degrade");
    assert.equal(buildPipeline(["header-names"], "fail").requests[0].name, "header-names");
  });
});

describe("the resource server and the rate limits", () => {
  const resource = new ResourceServer("http://127.0.0.1:9/mcp", ["http://127.0.0.1:9"], ["openid"], "web-login");

  test("cheap checks refuse before any key is fetched", async () => {
    const jwt = (claims: object) => ["eyJhbGciOiJSUzI1NiIsImtpZCI6ImsxIn0",
                                     Buffer.from(JSON.stringify(claims)).toString("base64url"), "c2ln"].join(".");
    const refused = (error: string | null) => (err: unknown) => err instanceof TokenRejected && err.error === error;
    await assert.rejects(resource.verify(undefined), refused(null));
    await assert.rejects(resource.verify("Basic abc"), refused("invalid_token"));
    await assert.rejects(resource.verify("Bearer not-a-jwt"), refused("invalid_token"));
    await assert.rejects(resource.verify("Bearer " + jwt({ iss: "https://other.example", aud: "http://127.0.0.1:9/mcp" })),
                         refused("invalid_token"));
    await assert.rejects(resource.verify("Bearer " + jwt({ iss: "http://127.0.0.1:9", aud: "http://127.0.0.1:9/other" })),
                         refused("invalid_token"));
    // Exactly one audience: a token that also names another resource could
    // be replayed here by that resource, so containing MCP_RESOURCE is not enough.
    await assert.rejects(resource.verify("Bearer " + jwt({ iss: "http://127.0.0.1:9",
                                                         aud: ["http://127.0.0.1:9/mcp", "https://other.example/api"] })),
                         refused("invalid_token"));
  });

  test("metadata, challenge and the audience's case", () => {
    assert.deepEqual([...resource.metadataPaths], ["/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"]);
    assert.equal(resource.challenge("invalid_token"),
                 'Bearer resource_metadata="http://127.0.0.1:9/.well-known/oauth-protected-resource/mcp", scope="openid", error="invalid_token"');
    assert.equal(canonical("HTTPS://MCP.Example.com/Mcp"), "https://mcp.example.com/Mcp");
    assert.throws(() => new ResourceServer("http://example.com/mcp", ["https://idp.example"], [], undefined), /https/);
  });

  test("a token bucket per key, refilled continuously, bounded", () => {
    let now = 0;
    const limiter = new RateLimiter(2, 2, () => now);
    assert.deepEqual([limiter.allow("a"), limiter.allow("a")], [[true, 0], [true, 0]]);
    assert.deepEqual(limiter.allow("a"), [false, 30]);
    assert.equal(limiter.allow("b")[0], true);
    now = 30;
    assert.equal(limiter.allow("a")[0], true);
    limiter.allow("c");
    limiter.allow("c");
    assert.equal(limiter.allow("b")[0], true, "b was evicted and comes back full");
    assert.deepEqual(new RateLimiter(0).allow("x"), [true, 0]);
  });

  test("pending sign-ins expire and are evicted from the front", () => {
    let now = 0;
    const pending = new PendingLogins(10, 3, () => now);
    const attempt = { state: "s", nonce: "n", verifier: "v", returnTo: "/" };
    for (let i = 0; i < 3; i++) pending.put(attempt);
    now = 20;
    const kept = pending.put({ ...attempt, state: "fresh" });
    assert.equal(pending.size, 1, "expired sign-ins go on the next put");
    for (let i = 0; i < 3; i++) pending.put(attempt);
    assert.equal(pending.size, 3);
    assert.equal(pending.take(kept), undefined, "at the limit the oldest goes first");
  });

  test("/auth/login and /auth/callback share the per-address limit", async () => {
    // An identity provider nobody answers on: an allowed sign-in is 502, a limited one 429.
    const login = new LoginTransport(new Provider("http://127.0.0.1:9", "web-login", undefined, "http://127.0.0.1/auth/callback", "openid"),
                                     randomBytes(32), false);
    const server = createServer(createGateway({
      pool: {} as pg.Pool, channel: flaky(null, 0).channel, pipeline: EMPTY_PIPELINE, login,
      perIp: new RateLimiter(2), perClient: new RateLimiter(0), allowedOrigins: new Set(), log: quiet,
    }));
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    try {
      const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
      const statuses = [];
      for (const path of ["/auth/login", "/auth/login", "/auth/login", "/auth/callback?code=x&state=y"]) {
        const response = await fetch(base + path, { redirect: "manual" });
        statuses.push(response.status);
        if (response.status === 429) {
          assert.match(response.headers.get("content-type") ?? "", /^application\/problem\+json/);
          assert.equal((await response.json()).code, "pgmi.rate_limited");
          assert.ok(Number(response.headers.get("retry-after")) >= 1);
        } else {
          await response.body?.cancel();
        }
      }
      assert.deepEqual(statuses, [502, 502, 429, 429]);
    } finally {
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
    }
  });
});

describe("a route-changing interceptor", () => {
  test("restates Mcp-Method and Mcp-Name from the rewritten body for the second policy call", async () => {
    const seen: McpHeaders[] = [];
    const channel: Channel = {
      ...flaky(null, 0).channel,
      mcpPolicy: async (_c, _r, _i, headers) => {
        seen.push(headers);
        return { ...NO_ROUTE, modern: true };
      },
    };
    const rename: RequestInterceptor = {
      name: "rename", needsJson: true, failure: "fail", timeoutMs: 1000, applies: () => true,
      run: async (message) => { ((message.json as { params: { name: string } }).params).name = "rewritten_tool"; },
    };
    const server = createServer(createGateway({
      pool: {} as pg.Pool, channel, pipeline: { requests: [rename], responses: [] }, perIp: new RateLimiter(0),
      perClient: new RateLimiter(0), allowedOrigins: new Set(), requestDeadlineMs: 30_000, log: quiet,
    }));
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    try {
      await fetch(`http://127.0.0.1:${(server.address() as AddressInfo).port}/mcp`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call",
                   "Mcp-Name": "echo_note", "X-Connection": "cn_test" },
        body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: "echo_note", arguments: {} } }),
      });
    } finally {
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
    }
    assert.equal(seen.length, 2);
    assert.deepEqual(seen[1], ["2026-07-28", "tools/call", "rewritten_tool"]);
  });
});

describe("the request deadline", () => {
  // A channel whose calls end the way the deadline ends them: DeadlineExceeded
  // from the database, or a wait that only the request's signal stops.
  function serveWith(channel: Channel, requestDeadlineMs = 30_000) {
    return createServer(createGateway({
      pool: {} as pg.Pool, channel, pipeline: EMPTY_PIPELINE, perIp: new RateLimiter(0), perClient: new RateLimiter(0),
      allowedOrigins: new Set(), requestDeadlineMs, log: quiet,
    }));
  }

  async function withServer(server: Server, run: (base: string) => Promise<void>) {
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    try {
      await run(`http://127.0.0.1:${(server.address() as AddressInfo).port}`);
    } finally {
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
    }
  }

  const cancelled: Channel = {
    ...flaky(null, 0).channel,
    restInvoke: async () => { throw new DeadlineExceeded(); },
    mcpPolicy: async () => { throw new DeadlineExceeded(); },
  };
  const waiting: Channel = {
    ...flaky(null, 0).channel,
    restInvoke: (c) => new Promise((_, reject) => c.signal.addEventListener("abort", () => reject(c.signal.reason), { once: true })),
  };

  test("a statement cancelled at the deadline is 504 pgmi.deadline_exceeded on REST", async () => {
    await withServer(serveWith(cancelled), async (base) => {
      const response = await fetch(base + "/api/x");
      assert.equal(response.status, 504);
      assert.match(response.headers.get("content-type") ?? "", /^application\/problem\+json/);
      assert.equal((await response.json()).code, "pgmi.deadline_exceeded");
      assert.match(response.headers.get("x-request-id") ?? "", /^[0-9a-f]{32}$/);
    });
  });

  test("on MCP it is 504 with -32603, the request's id and requestId", async () => {
    await withServer(serveWith(cancelled), async (base) => {
      const response = await fetch(base + "/mcp", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ jsonrpc: "2.0", id: 7, method: "tools/call", params: { name: "t" } }),
      });
      assert.equal(response.status, 504);
      const envelope = await response.json();
      assert.equal(envelope.id, 7);
      assert.equal(envelope.error.code, -32603);
      assert.equal(envelope.error.data.code, "pgmi.deadline_exceeded");
      assert.match(envelope.error.data.requestId, /^[0-9a-f]{32}$/);
    });
  });

  test("the gateway's own deadline is 504 too, not an abandoned request", async () => {
    await withServer(serveWith(waiting, 50), async (base) => {
      const response = await fetch(base + "/api/x");
      assert.equal(response.status, 504);
      assert.equal((await response.json()).code, "pgmi.deadline_exceeded");
    });
  });
});

const databaseUrl = process.env.DATABASE_URL;

describe("against the deployed template", { skip: databaseUrl ? false : "DATABASE_URL is not set" }, () => {
  let pool: pg.Pool;
  let server: Server;
  let base: string;
  let session: string;
  let csrf: string;

  function serve(gatewayPool: pg.Pool, channel: Channel = postgresChannel(gatewayPool)) {
    return createServer(createGateway({
      pool: gatewayPool, channel, pipeline: EMPTY_PIPELINE, login: new LoginTransport(undefined, randomBytes(32), true),
      perIp: new RateLimiter(0), perClient: new RateLimiter(0), allowedOrigins: new Set(), log: quiet,
    }));
  }

  async function call(method: string, path: string, init: { body?: unknown; headers?: Record<string, string>; signedIn?: boolean } = {}) {
    const headers: Record<string, string> = { Accept: "application/json", ...init.headers };
    if (init.body !== undefined) headers["Content-Type"] = "application/json";
    if (init.signedIn) headers.Cookie = `__Host-session=${session}`;
    const response = await fetch(base + path, { method, headers, body: init.body === undefined ? undefined : JSON.stringify(init.body) });
    const text = await response.text();
    return { status: response.status, headers: response.headers, json: text ? JSON.parse(text) : undefined };
  }

  const post = (path: string, body: unknown, headers: Record<string, string> = {}) =>
    call("POST", path, { body, headers: { "X-CSRF-Token": csrf, ...headers }, signedIn: true });

  function assertProblem(response: Awaited<ReturnType<typeof call>>, status: number, code: string) {
    assert.equal(response.status, status, JSON.stringify(response.json));
    assert.match(response.headers.get("content-type") ?? "", /^application\/problem\+json/);
    assert.equal(response.json.code, code);
  }

  function raw(method: string, path: string, headers: OutgoingHttpHeaders, body?: Buffer) {
    return new Promise<{ status: number; headers: Record<string, string | string[] | undefined>; body: Buffer }>((resolve, reject) => {
      const req = request(base + path, { method, headers }, (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (c: Buffer) => chunks.push(c));
        res.on("end", () => resolve({ status: res.statusCode!, headers: res.headers, body: Buffer.concat(chunks) }));
      });
      req.on("error", reject);
      req.end(body);
    });
  }

  before(async () => {
    pool = createPool(databaseUrl!);
    server = serve(pool);
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;

    // Development sign-in. In production the gateway runs OIDC and calls
    // api.open_login_session instead.
    const signIn = await fetch(base + "/auth/dev-login", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ subject: "client-typescript", email: "client-typescript@example.com", displayName: "TypeScript client" }),
    });
    assert.equal(signIn.status, 204, await signIn.text());
    const cookies = new Map(signIn.headers.getSetCookie().map((c) => c.split(";")[0].split("=") as [string, string]));
    session = cookies.get("__Host-session")!;
    csrf = cookies.get("__Host-csrf")!;
  });

  after(async () => {
    await new Promise((resolve) => server.close(resolve));
    await pool.end();
  });

  test("HEAD carries the GET Content-Length and no content", async () => {
    const get = await raw("GET", "/api/openapi.json", {});
    const head = await raw("HEAD", "/api/openapi.json", {});
    assert.equal(head.status, 200);
    assert.equal(head.headers["content-length"], String(get.body.length));
    assert.equal(head.body.length, 0);
  });

  test("an exhausted pool is 503 after the acquire timeout", async () => {
    const small = new pg.Pool({ connectionString: databaseUrl, max: 1, connectionTimeoutMillis: 300 });
    const held = await small.connect();
    const busy = serve(small);
    await new Promise<void>((resolve) => busy.listen(0, "127.0.0.1", resolve));
    try {
      const response = await fetch(`http://127.0.0.1:${(busy.address() as AddressInfo).port}/api/hello`);
      assert.equal(response.status, 503);
      assert.equal(response.headers.get("retry-after"), "1");
      assert.equal((await response.json()).code, "pgmi.unavailable");
    } finally {
      held.release();
      await new Promise((resolve) => busy.close(resolve));
      await small.end();
    }
  });

  test("a server that cannot be reached is 503, not a lost connection retried", async () => {
    const dead = new pg.Pool({ connectionString: "postgresql://x:y@127.0.0.1:9/none", connectionTimeoutMillis: 300 });
    const down = serve(dead, connectionRetry(postgresChannel(dead), 2, 0, quiet));
    await new Promise<void>((resolve) => down.listen(0, "127.0.0.1", resolve));
    try {
      const response = await fetch(`http://127.0.0.1:${(down.address() as AddressInfo).port}/api/hello`);
      assert.equal(response.status, 503);
      assert.equal((await response.json()).code, "pgmi.unavailable");
    } finally {
      await new Promise((resolve) => down.close(resolve));
      await dead.end();
    }
  });

  test("a body over 1 MiB is 413, declared or chunked", async () => {
    const big = Buffer.alloc(MAX_BODY_BYTES + 1, "a");
    for (const headers of [{ "Content-Type": "application/json" },
                           { "Content-Type": "application/json", "Transfer-Encoding": "chunked" }]) {
      const response = await raw("POST", "/api/echo", headers, big);
      assert.equal(response.status, 413);
      assert.equal(JSON.parse(response.body.toString()).code, "pgmi.body_too_large");
    }
  });

  test("repeated headers: a list is joined, Cookie too, a single-valued one is refused", async () => {
    assert.equal((await raw("GET", "/api/hello", { Accept: ["application/json", "*/*"] })).status, 200);
    assert.equal((await raw("GET", "/api/me", { Cookie: ["theme=dark", `__Host-session=${session}`] })).status, 200);
    const refused = await raw("GET", "/api/hello", { "X-Organization-Session": ["a", "b"] });
    assert.equal(refused.status, 400);
    assert.equal(JSON.parse(refused.body.toString()).code, "pgmi.header_collision");
  });

  test("a read-only route goes through the channel: policy, then invoke", async () => {
    const lines: string[] = [];
    const traced = serve(pool, tracing(postgresChannel(pool), (l) => lines.push(l)));
    await new Promise<void>((resolve) => traced.listen(0, "127.0.0.1", resolve));
    try {
      const response = await fetch(`http://127.0.0.1:${(traced.address() as AddressInfo).port}/api/hello?name=x`);
      assert.equal(response.status, 200);
      assert.deepEqual(lines.map((l) => l.split(" ")[2]), ["rest.policy", "rest.invoke"]);
    } finally {
      await new Promise((resolve) => traced.close(resolve));
    }
  });

  test("logout closes the session and clears both cookies", async () => {
    const signIn = await fetch(base + "/auth/dev-login", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ subject: "client-typescript-logout", email: "client-typescript-logout@example.com" }),
    });
    assert.equal(signIn.status, 204);
    const cookies = new Map(signIn.headers.getSetCookie().map((c) => c.split(";")[0].split("=") as [string, string]));
    const jar = `__Host-session=${cookies.get("__Host-session")}; __Host-csrf=${cookies.get("__Host-csrf")}`;

    assertProblem(await call("POST", "/auth/logout", { headers: { Cookie: jar } }), 403, "pgmi.csrf_refused");
    assertProblem(await call("POST", "/auth/logout", { headers: { Cookie: jar, "X-CSRF-Token": cookies.get("__Host-csrf")!, Origin: "https://evil.example" } }),
                  403, "pgmi.origin_refused");
    const logout = await fetch(base + "/auth/logout", { method: "POST", headers: { Cookie: jar, "X-CSRF-Token": cookies.get("__Host-csrf")! } });
    assert.equal(logout.status, 204);
    assert.equal(logout.headers.get("cache-control"), "no-store");
    assert.deepEqual(logout.headers.getSetCookie(), [
      "__Host-session=; Path=/; Max-Age=0; Secure; SameSite=Strict; HttpOnly",
      "__Host-csrf=; Path=/; Max-Age=0; Secure; SameSite=Strict",
    ]);
    assert.equal((await call("GET", "/api/me", { headers: { Cookie: jar } })).status, 401);
  });

  test("openapi.json lists what the caller may call", async () => {
    const anonymous = await call("GET", "/api/openapi.json");
    assert.equal(anonymous.status, 200);
    assert.ok("/hello" in anonymous.json.paths);
    assert.ok(!("/me" in anonymous.json.paths));
    const signedIn = await call("GET", "/api/openapi.json", { signedIn: true });
    assert.ok("/me" in signedIn.json.paths);
  });

  test("a public route, an authenticated route, a dropped client session header", async () => {
    const hello = await call("GET", "/api/hello?name=TypeScript");
    assert.equal(hello.status, 200);
    assert.equal(hello.json.message, "Hello, TypeScript!");
    assert.equal((await call("GET", "/api/me")).status, 401);
    assert.equal((await call("GET", "/api/me", { signedIn: true })).status, 200);
    assert.equal((await call("GET", "/api/me", { headers: { "X-Login-Session": session } })).status, 401);
  });

  test("coded problems", async () => {
    assertProblem(await call("POST", "/api/connections", { body: { clientName: "x" }, signedIn: true }), 403, "pgmi.csrf_refused");
    assertProblem(await post("/api/connections", { clientName: 5 }), 400, "pgmi.invalid_body");
    assertProblem(await post("/api/connections", { clientName: "x", organizationId: "00000000-0000-4000-8000-000000000000", roleId: "agent.reader" }),
                  422, "membership.connection.not_member");

    const created = await post("/api/organizations", { name: "TypeScript client", slug: "ts-" + randomBytes(4).toString("hex") });
    assert.equal(created.status, 201);
    const opened = await post("/api/organization-sessions", { organizationId: created.json.id });
    assert.equal(opened.status, 200);
    assertProblem(await post("/api/connections", { clientName: "x", validDays: 999, organizationId: created.json.id, roleId: "agent.reader" },
                             { "x-organization-session": opened.json.organizationSession }),
                  400, "pgmi.invalid_body");
  });

  test("MCP over a connection handle: the approved agent acts, a notification is 202", async () => {
    const created = await post("/api/organizations", { name: "TypeScript MCP", slug: "tsm-" + randomBytes(4).toString("hex") });
    const opened = await post("/api/organization-sessions", { organizationId: created.json.id });
    const connection = await post("/api/connections", { clientName: "ts", organizationId: created.json.id, roleId: "agent.reader" },
                                  { "x-organization-session": opened.json.organizationSession });
    assert.equal(connection.status, 201, JSON.stringify(connection.json));
    const mcp = (body: object, headers: Record<string, string> = {}) =>
      fetch(base + "/mcp", { method: "POST", headers: { "Content-Type": "application/json", "X-Connection": connection.json.connection, ...headers },
                             body: JSON.stringify(body) });
    const listed = await mcp({ jsonrpc: "2.0", id: 1, method: "tools/list" }, { "MCP-Protocol-Version": "2025-11-25" });
    assert.equal(listed.status, 200);
    assert.equal(listed.headers.get("mcp-protocol-version"), "2025-11-25");
    assert.ok(Array.isArray((await listed.json()).result.tools));
    const note = await mcp({ jsonrpc: "2.0", method: "notifications/initialized" });
    assert.equal(note.status, 202);
    assert.equal(await note.text(), "");
    const bearer = await mcp({ jsonrpc: "2.0", id: 2, method: "tools/list" },
                             { "MCP-Protocol-Version": "2025-11-25", "X-Connection": "", Authorization: "Bearer " + connection.json.connection });
    assert.equal(bearer.status, 200);
    const unknown: Record<string, string>[] = [{ "X-Connection": "cn_unknown" }, { "X-Connection": "", Authorization: "Bearer cn_unknown" }];
    for (const sent of unknown) {
      const refused = await mcp({ jsonrpc: "2.0", id: 3, method: "tools/list" }, { "MCP-Protocol-Version": "2025-11-25", ...sent });
      assert.equal(refused.status, 401);
      assert.equal(refused.headers.get("www-authenticate"), 'Bearer error="invalid_token"');
    }
  });

  test("application_name and a bounded pool", async () => {
    await call("GET", "/api/hello");
    const { rows } = await pool.query(
      "SELECT count(*)::int AS n FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1",
      [APPLICATION_NAME]);
    assert.ok(rows[0].n >= 1 && rows[0].n <= 10, `connections named ${APPLICATION_NAME}: ${rows[0].n}`);
  });

  test("cancelBackend stops a running statement in moments, from outside the pool", async () => {
    const one = createPool(databaseUrl!, 1);
    const client = await acquire(one);
    try {
      const { rows: [{ pid }] } = await client.query<{ pid: number }>("SELECT pg_backend_pid() AS pid");
      const started = Date.now();
      const cancelled = assert.rejects(client.query("SELECT pg_sleep(5)"), (err: pg.DatabaseError) => err.code === "57014");
      await new Promise((resolve) => setTimeout(resolve, 100));
      await cancelBackend(one, pid, "r1");
      await cancelled;
      assert.ok(Date.now() - started < 2_000);
    } finally {
      client.release();
      await one.end();
    }
  });

  test("statement_timeout is local to the call's transaction; a spent deadline sends nothing", async () => {
    const one = createPool(databaseUrl!, 1);
    try {
      const channel = postgresChannel(one);
      await channel.restPolicy(ctx({ method: "GET", url: "/hello", deadline: Date.now() + 1_500 }), null);
      assert.equal((await one.query("SHOW statement_timeout")).rows[0].statement_timeout, "0");
      await assert.rejects(channel.restPolicy(ctx({ method: "GET", url: "/hello", deadline: Date.now() - 1 }), null),
                           DeadlineExceeded);
    } finally {
      await one.end();
    }
  });
});
