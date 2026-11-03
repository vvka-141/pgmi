// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
//   DATABASE_URL=postgresql://gateway:secret@localhost:5432/mydb npm start
//
// The environment variables are the same in every reference gateway; the
// table is in ../../PROTOCOL.md ("Configuration"). This file reads them,
// refuses a value it cannot use (a typo is a startup error, never a NaN or a
// silent default), composes the two extension seams once and listens.

import { createServer } from "node:http";
import { connectionRetry, createPool, postgresChannel, serializationRetry, tracing } from "./channel.js";
import { createGateway } from "./gateway.js";
import { buildPipeline, type FailurePolicy } from "./interceptors.js";
import { LoginTransport } from "./login.js";
import { RateLimiter, ResourceServer } from "./resource-server.js";


const env = process.env;

function fail(message: string): never {
  console.error(`Error: ${message}`);
  process.exit(1);
}

function integer(name: string, fallback: number, min = 0): number {
  const raw = env[name];
  if (raw === undefined || raw === "") return fallback;
  if (!/^\d+$/.test(raw) || Number(raw) < min) fail(`${name} must be a whole number of at least ${min}, not ${JSON.stringify(raw)}`);
  return Number(raw);
}

function seconds(name: string, fallback: number): number {
  const raw = env[name];
  if (raw === undefined || raw === "") return fallback;
  if (!/^\d+(\.\d+)?$/.test(raw)) fail(`${name} must be a number of seconds, not ${JSON.stringify(raw)}`);
  return Number(raw);
}

const databaseUrl = env.DATABASE_URL;
if (!databaseUrl) fail("DATABASE_URL is required, e.g. postgresql://postgres:postgres@localhost:5432/mydb");
const host = env.HOST || "127.0.0.1";
const port = integer("PORT", 8080, 1);
const origins = env.ALLOWED_ORIGINS?.split(",").map((o) => o.trim()).filter(Boolean)
  ?? [`http://localhost:${port}`, `http://127.0.0.1:${port}`];
const embedFailure = env.EMBED_FAILURE_POLICY || "fail";
if (embedFailure !== "fail" && embedFailure !== "degrade") fail("EMBED_FAILURE_POLICY must be fail or degrade");
const interceptorNames = (env.INTERCEPTORS ?? "").split(",").map((n) => n.trim()).filter(Boolean);

let login, resource, pipeline;
try {
  login = LoginTransport.fromEnvironment(env);
  resource = ResourceServer.fromEnvironment(env);
  pipeline = buildPipeline(interceptorNames, embedFailure as FailurePolicy);
} catch (err) {
  fail((err as Error).message);
}

const pool = createPool(databaseUrl, integer("POOL_MAX_SIZE", 10, 1));
// ADAPT: the decorators your gateway needs, in one place. Both retries wrap
// the tracing, so each attempt is a log line of its own.
const channel = connectionRetry(serializationRetry(tracing(postgresChannel(pool)),
                                                   integer("RETRY_MAX_ATTEMPTS", 3, 1),
                                                   seconds("RETRY_BASE_DELAY", 0.05) * 1000));

const server = createServer(createGateway({
  pool, channel, pipeline, login, resource,
  perIp: new RateLimiter(integer("RATE_LIMIT_PER_IP", 600)),
  perClient: new RateLimiter(integer("RATE_LIMIT_PER_CLIENT", 300)),
  allowedOrigins: new Set(origins),
}));
// A client that stops sending headers or a body never holds the server.
server.headersTimeout = 10_000;
server.requestTimeout = 30_000;

server.listen(port, host, () => {
  console.log(`[gateway] listening on http://${host}:${port}, database ${new URL(databaseUrl).host}`);
  if (login?.provider) console.log(`[gateway] sign-in via ${login.provider.issuer}`);
  if (login?.devLogin) console.log("[gateway] POST /auth/dev-login is on (DEV_LOGIN=1)");
  if (resource) console.log(`[gateway] /mcp is an OAuth resource server for ${resource.resource}`);
  if (interceptorNames.length) console.log(`[gateway] interceptors: ${interceptorNames.join(", ")}`);
});

for (const signal of ["SIGTERM", "SIGINT"] as const) {
  process.on(signal, () => server.close(() => void pool.end()));
}
