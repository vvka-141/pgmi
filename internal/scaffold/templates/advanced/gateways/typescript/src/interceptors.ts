// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project: it is a baseline to learn from,
// not a library. The rules it keeps are in ../../PROTOCOL.md ("Extension seams").
//
// Request and response interceptors: an ordered pipeline around each dispatch.
// A request interceptor runs after the policy call and before the transaction
// opens; it may change the method, URL, headers and body. A response
// interceptor runs after the commit; it may change the status, headers and
// body. Uses: embedding vectors for text fields, enrichment from another
// service, signing storage URLs the handler returned, redaction.
//
// The rules:
// - The database still decides. An interceptor never decides access or
//   validity; it selects itself from what the policy call returned (the
//   handler, the x-* annotations on its inputSchema), never from a path list.
// - Zero cost when unused. No interceptor applies: the body passes as bytes,
//   never parsed. Some need JSON: it is parsed once and serialized once.
// - Failure policy per interceptor. "fail" (default) answers 502
//   pgmi.interceptor_failed; "degrade" logs and goes on with the request as
//   it was before that interceptor. A timeout is a failure.
// - No database connection is held while an interceptor runs.
// - No per-request state in an interceptor: it is in the message and the
//   context. An outgoing client is created once at startup and shared.
//
// This file also holds the two examples INTERCEPTORS can enable: "embed"
// (embedFields with fakeEmbedder) and "request-id" (requestIdHeader).

import { createHash } from "node:crypto";
import type { Json, JsonObject, RequestContext } from "./channel.js";

export type FailurePolicy = "fail" | "degrade";

const UNPARSED = Symbol("unparsed");

// Headers and a body, the body readable as bytes or as parsed JSON. The JSON
// is parsed on first read and cached, so every interceptor shares one parse;
// finish() serializes it once. A body that is not JSON reads as null and
// passes on as the bytes it was: the database answers it.
export class Message {
  private parsedJson: Json | typeof UNPARSED;

  constructor(public headers: Record<string, string>, public body: Buffer | null, parsed: Json | typeof UNPARSED = UNPARSED) {
    this.parsedJson = parsed;
  }

  get json(): Json {
    if (this.parsedJson === UNPARSED) {
      try {
        this.parsedJson = this.body && this.body.length ? (JSON.parse(this.body.toString("utf8")) as Json) : null;
      } catch {
        this.parsedJson = null;
      }
    }
    return this.parsedJson;
  }

  set json(value: Json) {
    this.parsedJson = value;
  }

  get parsed(): boolean {
    return this.parsedJson !== UNPARSED;
  }

  finish(): void {
    if (this.parsedJson !== UNPARSED && this.parsedJson !== null) this.body = Buffer.from(JSON.stringify(this.parsedJson));
  }

  // degrade works on a copy, adopted only when the interceptor succeeds.
  copy(): this {
    const twin = Object.create(Object.getPrototypeOf(this) as object) as this;
    Object.assign(twin, this);
    twin.headers = { ...this.headers };
    twin.parsedJson = this.parsedJson === UNPARSED ? UNPARSED : structuredClone(this.parsedJson);
    return twin;
  }

  adopt(other: this): void {
    Object.assign(this, other);
  }
}

export class RequestMessage extends Message {
  constructor(public method: string, public url: string, headers: Record<string, string>, body: Buffer | null,
              parsed: Json | typeof UNPARSED = UNPARSED) {
    super(headers, body, parsed);
  }
}

export class ResponseMessage extends Message {
  constructor(public status: number, headers: Record<string, string>, body: Buffer | null,
              parsed: Json | typeof UNPARSED = UNPARSED) {
    super(headers, body, parsed);
  }
}

// name shows in the log. needsJson says whether it reads message.json; one
// that only touches headers leaves the body unparsed. applies() selects it
// from the context, which holds what the database published for the route.
// run() gets a signal that aborts at its timeout: pass it to every outgoing
// call, so an overrun stops its work instead of leaking it.
export interface Interceptor<M extends Message> {
  readonly name: string;
  readonly needsJson: boolean;
  readonly failure: FailurePolicy;
  readonly timeoutMs: number;
  applies(ctx: RequestContext): boolean;
  run(message: M, ctx: RequestContext, signal: AbortSignal): Promise<void>;
}

export type RequestInterceptor = Interceptor<RequestMessage>;
export type ResponseInterceptor = Interceptor<ResponseMessage>;

export class InterceptorFailed extends Error {
  // applied: the failure came after the commit, so the request took effect.
  constructor(readonly interceptor: string, readonly applied: boolean, options?: ErrorOptions) {
    super(`interceptor ${interceptor} failed`, options);
  }
}

// Read-only after startup: built once, shared by every request.
export interface Pipeline {
  readonly requests: readonly RequestInterceptor[];
  readonly responses: readonly ResponseInterceptor[];
}

export const EMPTY_PIPELINE: Pipeline = { requests: [], responses: [] };

export function selected<M extends Message>(interceptors: readonly Interceptor<M>[], ctx: RequestContext): Interceptor<M>[] {
  return interceptors.filter((i) => i.applies(ctx));
}

function overrun(signal: AbortSignal): Promise<never> {
  return new Promise((_, reject) => {
    const fail = () => reject(signal.reason instanceof Error ? signal.reason : new Error("aborted"));
    if (signal.aborted) fail();
    else signal.addEventListener("abort", fail, { once: true });
  });
}

// Runs the interceptors, in order. Each gets its own timeout, bounded by the
// request's deadline (ctx.signal); overrunning it is a failure even when the
// interceptor ignores its signal, because the race below stops waiting.
export async function runInterceptors<M extends Message>(interceptors: readonly Interceptor<M>[], message: M,
                                                         ctx: RequestContext, applied: boolean,
                                                         log: (line: string) => void = console.log): Promise<void> {
  for (const interceptor of interceptors) {
    const work = interceptor.failure === "degrade" ? message.copy() : message;
    const signal = AbortSignal.any([ctx.signal, AbortSignal.timeout(interceptor.timeoutMs)]);
    const remaining = Math.max(0, Math.min(ctx.deadline, Date.now() + interceptor.timeoutMs) - Date.now());
    try {
      if (interceptor.needsJson) void work.json;
      await Promise.race([interceptor.run(work, { ...ctx, deadline: Date.now() + remaining, signal }, signal), overrun(signal)]);
    } catch (err) {
      const reason = signal.aborted ? "timed out" : err instanceof Error ? `${err.name}: ${err.message}` : String(err);
      if (interceptor.failure === "degrade") {
        log(`[interceptor] ${ctx.requestId} ${interceptor.name} degraded (${reason})`);
        continue;
      }
      log(`[interceptor] ${ctx.requestId} ${interceptor.name} failed (${reason})`);
      throw new InterceptorFailed(interceptor.name, applied, { cause: err });
    }
    if (work !== message) message.adopt(work);
  }
  message.finish();
}

// The object the handler's inputSchema describes: the REST body, or an MCP
// call's params.arguments. null when there is none.
export function argumentsOf(message: Message, ctx: RequestContext): JsonObject | null {
  let value = message.json;
  if (ctx.protocol === "mcp") {
    const params = isObject(value) ? value.params : null;
    value = isObject(params) ? (params.arguments ?? null) : null;
  }
  return isObject(value) ? value : null;
}

export function isObject(value: Json | undefined): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// -- Example: a computed field for every property the schema marks x-embed ------

// The outside call behind an interface: a real one posts to an embedding API
// through the process's one keep-alive fetch dispatcher (see README.md);
// tests use the fake.
export interface Embedder {
  embed(texts: string[], signal: AbortSignal): Promise<number[][]>;
}

// Deterministic and offline, the same in every reference gateway: the first
// four bytes of the SHA-256 of the UTF-8 text, each divided by 255, rounded to
// four places. Like a real model with a token limit, it refuses a text over
// 2000 characters (code points, as Python counts them).
export const fakeEmbedder: Embedder = {
  async embed(texts) {
    return texts.map((text) => {
      if ([...text].length > 2000) throw new Error("text over 2000 characters");
      // toFixed rounds the exact binary value, as Python's round() does;
      // Math.round(x * 1e4) / 1e4 can be one digit off.
      return [...createHash("sha256").update(text, "utf8").digest().subarray(0, 4)].map((b) => Number((b / 255).toFixed(4)));
    });
  },
};

// For each inputSchema property marked "x-embed": true, adds a
// <property>_embedding sibling holding its vector. The schema declares the
// sibling too, so the database validates it like any other field. One batched
// call for all fields. A sibling already present is left alone: a client that
// embeds for itself is not embedded twice.
// ADAPT: an embedding outage can degrade (the handler searches without the
// vector) or fail; choose per project.
export function embedFields(embedder: Embedder, failure: FailurePolicy = "fail", timeoutMs = 2000): RequestInterceptor {
  return {
    name: "embed-fields",
    needsJson: true,
    failure,
    timeoutMs,
    applies: (ctx) => Object.values(ctx.annotations).some((a) => a["x-embed"] === true),
    async run(request, ctx, signal) {
      const args = argumentsOf(request, ctx);
      if (args === null) return;
      const fields = Object.entries(ctx.annotations)
        .filter(([name, a]) => a["x-embed"] === true && typeof args[name] === "string"
                && (args[name] as string).trim() !== "" && !(name + "_embedding" in args))
        .map(([name]) => name);
      if (fields.length === 0) return;
      const vectors = await embedder.embed(fields.map((name) => args[name] as string), signal);
      fields.forEach((name, i) => { args[name + "_embedding"] = vectors[i]; });
    },
  };
}

// -- Example: a response header --------------------------------------------------

// Answers the gateway's request id in X-Request-Id, the id its log lines
// carry. Headers only: the body is never parsed.
export function requestIdHeader(): ResponseInterceptor {
  return {
    name: "request-id",
    needsJson: false,
    failure: "fail",
    timeoutMs: 2000,
    applies: () => true,
    async run(response, ctx) {
      response.headers["x-request-id"] = ctx.requestId;
    },
  };
}

// The examples INTERCEPTORS can name, each a factory registered once.
// ADAPT: register your own here, each with its long-lived dependencies (a
// signing key from configuration, a client for another service).
export const EXAMPLES = new Map<string, (embedFailure: FailurePolicy) => { request?: RequestInterceptor; response?: ResponseInterceptor }>([
  ["embed", (embedFailure) => ({ request: embedFields(fakeEmbedder, embedFailure) })],
  ["request-id", () => ({ response: requestIdHeader() })],
]);

// The pipeline INTERCEPTORS names, in its order. Unset, none run and every
// body passes as bytes. An unknown name is a startup error, never ignored.
export function buildPipeline(names: readonly string[], embedFailure: FailurePolicy): Pipeline {
  const requests: RequestInterceptor[] = [];
  const responses: ResponseInterceptor[] = [];
  for (const name of names) {
    const factory = EXAMPLES.get(name);
    if (factory === undefined) throw new Error(`unknown interceptor ${name}; known: ${[...EXAMPLES.keys()].join(", ")}`);
    const { request, response } = factory(embedFailure);
    if (request) requests.push(request);
    if (response) responses.push(response);
  }
  return { requests, responses };
}
