// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project: it is a baseline to learn from,
// not a library. The rules it keeps are in ../../PROTOCOL.md and
// docs/advanced/LOGIN-TRANSPORT.md in the pgmi repository.
//
// The login transport: OIDC sign-in in front of the database. gateway.ts owns
// HTTP; this file owns the sign-in rules.
//
//   GET  /auth/login?returnTo=/path   start the authorization code flow (PKCE)
//   GET  /auth/callback               check state, exchange the code, verify the
//                                     ID token, call api.open_login_session
//   POST /auth/logout                 api.close_login_session, clear the cookies
//   POST /auth/dev-login              DEV_LOGIN=1 only: api.open_dev_login_session
//
// Only the opaque login-session handle reaches the browser, in an HttpOnly
// cookie. The database decides who the caller is; nothing here names a person.

import { createHash, createHmac, randomBytes, timingSafeEqual } from "node:crypto";
import { isIP } from "node:net";
import { createRemoteJWKSet, customFetch, errors, jwtVerify, type JWTPayload } from "jose";
import type pg from "pg";
import { acquire } from "./channel.js";

// ADAPT: cookie names and lifetimes.
export const SESSION_COOKIE = "__Host-session";
export const CSRF_COOKIE = "__Host-csrf";
export const LOGIN_COOKIE = "__Host-login";
export const CSRF_HEADER = "x-csrf-token";
const SESSION_MAX_AGE = 7 * 24 * 3600;
const LOGIN_MAX_AGE = 600;
// Standard OAuth/OIDC token verification, not pgmi-specific: replace it with
// your own stack's verifier. The same three algorithms in every gateway.
export const TOKEN_ALGORITHMS = ["RS256", "PS256", "ES256"];
export const CLOCK_SKEW_SECONDS = 60;
const PROVIDER_TIMEOUT_MS = 10_000;
// ADAPT: the development issuer seeded by membership/dev/01-issuers.sql.
const DEV_ISSUER = "dev";

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

// Session and connection handles the transport did not issue, the cookie that
// carries the one it did, and hop-by-hop headers.
const NOT_FORWARDED = new Set([
  "x-login-session", "x-connection", "cookie", CSRF_HEADER,
  "connection", "keep-alive", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade",
]);

// One of two values would be guessed at: refuse instead.
const SINGLE_VALUED = new Set([
  "authorization", "content-length", "content-type", "host", "origin",
  CSRF_HEADER, "x-organization-session", "x-pgmi-transaction-isolation",
]);

export class HeaderCollision extends Error {}

// A sign-in refused with a status and a login.* code.
export class LoginFailed extends Error {
  constructor(readonly status: number, readonly code: string, readonly detail: string, options?: ErrorOptions) {
    super(detail, options);
  }
}

// Every header the client sent: lower-cased, each name once, a repeated name
// joined (RFC 9110 §5.3), Cookie with "; ". Interceptors see this, credentials
// included. rawHeaders, because Node already joins or drops some repeats in
// req.headers.
export function incomingHeaders(rawHeaders: readonly string[]): Map<string, string> {
  const out = new Map<string, string>();
  for (let i = 0; i < rawHeaders.length; i += 2) {
    const name = rawHeaders[i].toLowerCase();
    const previous = out.get(name);
    out.set(name, previous === undefined ? rawHeaders[i + 1] : previous + (name === "cookie" ? "; " : ", ") + rawHeaders[i + 1]);
  }
  return out;
}

// pgmi-specific: the header rules of what may be forwarded; see
// ../../PROTOCOL.md "Identity". The headers the database sees: the incoming
// ones without the handles and cookie the client sent. A repeated SINGLE_VALUED name in any letter case is
// refused. The login session comes from the transport's cookie only, never
// the client.
export function forwardedHeaders(rawHeaders: readonly string[], loginSession: string | undefined): Map<string, string> {
  const seen = new Set<string>();
  for (let i = 0; i < rawHeaders.length; i += 2) {
    const name = rawHeaders[i].toLowerCase();
    if (SINGLE_VALUED.has(name) && seen.has(name)) throw new HeaderCollision(name);
    seen.add(name);
  }
  const out = incomingHeaders(rawHeaders);
  for (const name of NOT_FORWARDED) out.delete(name);
  if (loginSession) out.set("x-login-session", loginSession);
  return out;
}

// Standard web plumbing, not pgmi-specific: replace it with your framework's own.
export function readCookies(header: string | undefined): Map<string, string> {
  const jar = new Map<string, string>();
  for (const part of (header ?? "").split(";")) {
    const at = part.indexOf("=");
    if (at > 0) jar.set(part.slice(0, at).trim(), part.slice(at + 1).trim());
  }
  return jar;
}

// SameSite=Strict keeps the session off every cross-site request, a link
// included. The sign-in cookie is Lax: the provider's redirect back to
// /auth/callback is a cross-site navigation that must carry it.
export function setCookie(name: string, value: string, maxAge: number, httpOnly = true): string {
  const sameSite = name === LOGIN_COOKIE ? "Lax" : "Strict";
  const cookie = `${name}=${value}; Path=/; Max-Age=${maxAge}; Secure; SameSite=${sameSite}`;
  return httpOnly ? cookie + "; HttpOnly" : cookie;
}

export function clearCookie(name: string): string {
  return setCookie(name, "", 0, name !== CSRF_COOKIE);
}

function equal(a: string, b: string): boolean {
  const x = Buffer.from(a);
  const y = Buffer.from(b);
  return x.length === y.length && timingSafeEqual(x, y);
}

// A same-site path only: no scheme, no //host, no backslash, no control or
// non-ASCII character a browser might reinterpret.
// ADAPT: an application points this at its own start page. The API explorer
// shows the signed-in routes, so it confirms the sign-in.
const DEFAULT_RETURN_TO = "/api/docs";

export function safeReturnTo(value: string | null): string {
  if (!value || !value.startsWith("/") || value.startsWith("//") || value.includes("\\") || !/^[!-~]+$/.test(value)) return DEFAULT_RETURN_TO;
  return value;
}

export function isLoopback(host: string): boolean {
  const bare = host.replace(/^\[|\]$/g, "");
  if (bare === "localhost") return true;
  if (isIP(bare) === 4) return bare.startsWith("127.");
  return bare === "::1";
}

// https, or http on a loopback host (a local identity provider in tests).
export function checkedIssuerUrl(value: string, name: string): string {
  const url = new URL(value);
  if (url.protocol !== "https:" && !(url.protocol === "http:" && isLoopback(url.hostname))) {
    throw new Error(`${name} must be https (http only on a loopback host): ${value}`);
  }
  return value;
}

// api.open_login_session arguments from verified ID token claims.
// email_verified and amr go through as the provider sent them; a value of the
// wrong JSON type becomes NULL, which the database reads as unverified and as
// no second factor.
export function loginArguments(claims: JWTPayload): unknown[] {
  const amr = claims.amr;
  return [
    claims.iss, claims.sub,
    typeof claims.email === "string" ? claims.email : null,
    typeof claims.name === "string" ? claims.name : null,
    typeof claims.email_verified === "boolean" ? claims.email_verified : null,
    Array.isArray(amr) && amr.every((a) => typeof a === "string") ? amr : null,
  ];
}

// previous is the handle the browser sent to /auth/login. The callback cannot
// read it: it is the provider's cross-site redirect, and the SameSite=Strict
// session cookie does not travel on it.
interface PendingLogin { state: string; nonce: string; verifier: string; returnTo: string; previous?: string }

// Sign-ins between /auth/login and /auth/callback, each usable once, for at
// most ttlMs, at most limit of them (the oldest goes first). One event loop:
// no lock. A Map keeps insertion order, which is time order on a monotonic
// clock, so expiry and eviction take from the front without scanning. The
// per-IP limit on /auth keeps one address from filling it.
//
// ADAPT: a single-process demo. Behind a load balancer, pin /auth/* to one
// process, keep this in a shared store, or drop the store: carry the pending
// sign-in in a signed, short-lived __Host-login cookie instead.
export class PendingLogins {
  readonly #items = new Map<string, { at: number; login: PendingLogin }>();

  constructor(private readonly ttlMs = LOGIN_MAX_AGE * 1000, private readonly limit = 10_000,
              private readonly clock: () => number = () => performance.now()) {}

  get size(): number {
    return this.#items.size;
  }

  put(login: PendingLogin): string {
    const key = randomBytes(32).toString("base64url");
    const now = this.clock();
    for (const [k, v] of this.#items) {
      if (now - v.at <= this.ttlMs && this.#items.size < this.limit) break;
      this.#items.delete(k);
    }
    this.#items.set(key, { at: now, login });
    return key;
  }

  take(key: string | undefined): PendingLogin | undefined {
    if (!key) return undefined;
    const entry = this.#items.get(key);
    this.#items.delete(key);
    return entry && this.clock() - entry.at <= this.ttlMs ? entry.login : undefined;
  }
}

interface Discovery { issuer: string; authorization_endpoint: string; token_endpoint: string; jwks_uri: string }

// One OIDC issuer: discovery, the code exchange and ID token checks. Outgoing
// calls use the built-in fetch, whose global dispatcher is one keep-alive
// connection pool for the whole process.
export class Provider {
  #discovery: Promise<{ metadata: Discovery; keys: ReturnType<typeof createRemoteJWKSet> }> | undefined;

  constructor(readonly issuer: string, readonly clientId: string, readonly clientSecret: string | undefined,
              readonly redirectUri: string, readonly scopes: string) {
    checkedIssuerUrl(issuer, "OIDC_ISSUER");
  }

  async #json(url: string, init: RequestInit = {}): Promise<unknown> {
    let response: Response;
    try {
      response = await fetch(url, { ...init, headers: { Accept: "application/json", ...init.headers },
                                    signal: AbortSignal.timeout(PROVIDER_TIMEOUT_MS) });
    } catch (err) {
      throw new LoginFailed(502, "pgmi.login.provider_unavailable", "the provider could not be reached", { cause: err });
    }
    if (!response.ok) throw new LoginFailed(401, "pgmi.login.provider_refused", `the provider answered ${response.status}`);
    try {
      return await response.json();
    } catch (err) {
      throw new LoginFailed(502, "pgmi.login.provider_unavailable", "the provider answered no JSON", { cause: err });
    }
  }

  // Discovery once; a failure is not cached, the next sign-in tries again.
  discovery() {
    this.#discovery ??= (async () => {
      const metadata = await this.#json(this.issuer.replace(/\/$/, "") + "/.well-known/openid-configuration") as Discovery;
      if (metadata?.issuer !== this.issuer || typeof metadata.jwks_uri !== "string") {
        throw new LoginFailed(502, "pgmi.login.provider_unavailable", "discovery names another issuer");
      }
      // An unknown kid refetches the key set at once (cooldown 0): after a
      // key rotation the next sign-in must not wait. Safe here, because ID
      // tokens reach this gateway only from the token endpoint, never from a
      // client; resource-server.ts, which takes tokens from anyone, bounds it.
      const keys = createRemoteJWKSet(new URL(metadata.jwks_uri), {
        cooldownDuration: 0, cacheMaxAge: 300_000, timeoutDuration: PROVIDER_TIMEOUT_MS,
        [customFetch]: async (url, init) => {
          let response: Response;
          try {
            response = await fetch(url, init);
          } catch (err) {
            throw new LoginFailed(502, "pgmi.login.provider_unavailable", "the key set could not be read", { cause: err });
          }
          // jose reports a non-200 key set as a token error; it is the provider's.
          if (!response.ok) throw new LoginFailed(502, "pgmi.login.provider_unavailable", "the key set could not be read");
          return response;
        },
      });
      return { metadata, keys };
    })();
    this.#discovery.catch(() => { this.#discovery = undefined; });
    return this.#discovery;
  }

  async authorizationUrl(state: string, nonce: string, challenge: string): Promise<string> {
    const { metadata } = await this.discovery();
    const query = new URLSearchParams({
      response_type: "code", client_id: this.clientId, redirect_uri: this.redirectUri, scope: this.scopes,
      state, nonce, code_challenge: challenge, code_challenge_method: "S256",
    });
    return metadata.authorization_endpoint + "?" + query.toString();
  }

  async exchange(code: string, verifier: string): Promise<string> {
    const { metadata } = await this.discovery();
    const form = new URLSearchParams({ grant_type: "authorization_code", code, redirect_uri: this.redirectUri, code_verifier: verifier });
    const headers: Record<string, string> = { "Content-Type": "application/x-www-form-urlencoded" };
    if (this.clientSecret) {
      // client_secret_basic: each part form-encoded first (RFC 6749 §2.3.1).
      const pair = encodeURIComponent(this.clientId) + ":" + encodeURIComponent(this.clientSecret);
      headers.Authorization = "Basic " + Buffer.from(pair).toString("base64");
    } else {
      form.set("client_id", this.clientId);
    }
    const tokens = await this.#json(metadata.token_endpoint, { method: "POST", headers, body: form.toString() });
    const idToken = (tokens as { id_token?: unknown } | null)?.id_token;
    if (typeof idToken !== "string") throw new LoginFailed(401, "pgmi.login.token_invalid", "the provider returned no ID token");
    return idToken;
  }

  async verify(idToken: string, nonce: string): Promise<JWTPayload> {
    const { keys } = await this.discovery();
    let claims: JWTPayload;
    try {
      ({ payload: claims } = await jwtVerify(idToken, keys, {
        algorithms: TOKEN_ALGORITHMS, audience: this.clientId, issuer: this.issuer, clockTolerance: CLOCK_SKEW_SECONDS,
        requiredClaims: ["iss", "sub", "aud", "exp", "iat", "nonce"],
      }));
    } catch (err) {
      if (err instanceof LoginFailed) throw err;
      if (err instanceof errors.JWKSTimeout) throw new LoginFailed(502, "pgmi.login.provider_unavailable", "the key set timed out", { cause: err });
      if (err instanceof errors.JOSEError) throw new LoginFailed(401, "pgmi.login.token_invalid", err.code, { cause: err });
      throw err;
    }
    // jose checks iat only for type; an ID token from the future is refused.
    if (typeof claims.iat !== "number" || claims.iat > Date.now() / 1000 + CLOCK_SKEW_SECONDS) {
      throw new LoginFailed(401, "pgmi.login.token_invalid", "iat is in the future");
    }
    if (Array.isArray(claims.aud) && claims.aud.length > 1 && claims.azp !== this.clientId) {
      throw new LoginFailed(401, "pgmi.login.token_invalid", "azp does not name this client");
    }
    if (typeof claims.nonce !== "string" || !equal(claims.nonce, nonce)) {
      throw new LoginFailed(401, "pgmi.login.token_invalid", "nonce mismatch");
    }
    if (typeof claims.sub !== "string") throw new LoginFailed(401, "pgmi.login.token_invalid", "sub is not a string");
    return claims;
  }
}

export interface Redirect { location: string; cookies: string[] }

export class LoginTransport {
  readonly #pending = new PendingLogins();

  constructor(readonly provider: Provider | undefined, private readonly csrfSecret: Buffer, readonly devLogin: boolean) {}

  // undefined when neither OIDC_ISSUER nor DEV_LOGIN is set: /api is then
  // anonymous. With DEV_LOGIN alone there is no provider.
  static fromEnvironment(env: NodeJS.ProcessEnv): LoginTransport | undefined {
    const issuer = env.OIDC_ISSUER;
    const devLogin = env.DEV_LOGIN === "1";
    if (!issuer && !devLogin) return undefined;
    let provider: Provider | undefined;
    if (issuer) {
      const missing = ["OIDC_CLIENT_ID", "OIDC_REDIRECT_URI"].filter((n) => !env[n]);
      if (missing.length) throw new Error(`OIDC_ISSUER is set but ${missing.join(", ")} is not`);
      provider = new Provider(issuer, env.OIDC_CLIENT_ID!, env.OIDC_CLIENT_SECRET || undefined, env.OIDC_REDIRECT_URI!,
                              env.OIDC_SCOPES ?? "openid email profile");
    }
    // ADAPT: set LOGIN_CSRF_SECRET when more than one process serves the same users.
    const secret = env.LOGIN_CSRF_SECRET ? Buffer.from(env.LOGIN_CSRF_SECRET) : randomBytes(32);
    return new LoginTransport(provider, secret, devLogin);
  }

  // Standard web plumbing, not pgmi-specific: replace it with your framework's own.
  csrfToken(loginSession: string): string {
    return createHmac("sha256", this.csrfSecret).update("csrf\0" + loginSession).digest("base64url");
  }

  // A state-changing request riding on the session cookie must carry the
  // session's CSRF token, which a cross-site page cannot read.
  csrfOk(method: string, loginSession: string | undefined, presented: string | undefined): boolean {
    if (SAFE_METHODS.has(method) || !loginSession) return true;
    return !!presented && equal(this.csrfToken(loginSession), presented);
  }

  async begin(provider: Provider, returnTo: string | null, cookies: Map<string, string>): Promise<Redirect> {
    const verifier = randomBytes(48).toString("base64url");
    const challenge = createHash("sha256").update(verifier).digest("base64url");
    const state = randomBytes(32).toString("base64url");
    const nonce = randomBytes(32).toString("base64url");
    const location = await provider.authorizationUrl(state, nonce, challenge);
    const key = this.#pending.put({ state, nonce, verifier, returnTo: safeReturnTo(returnTo), previous: cookies.get(SESSION_COOKIE) });
    return { location, cookies: [setCookie(LOGIN_COOKIE, key, LOGIN_MAX_AGE)] };
  }

  // The pending sign-in is consumed first, so a callback works once. The
  // handle the browser held at /auth/login is closed: each sign-in gets a new one.
  async complete(provider: Provider, cookies: Map<string, string>, query: URLSearchParams, pool: pg.Pool): Promise<Redirect> {
    const attempt = this.#pending.take(cookies.get(LOGIN_COOKIE));
    const state = query.get("state");
    if (attempt === undefined || !state || !equal(state, attempt.state)) {
      throw new LoginFailed(400, "pgmi.login.state_invalid", "unknown, expired or replayed sign-in");
    }
    const error = query.get("error");
    if (error) throw new LoginFailed(400, "pgmi.login.provider_error", error.slice(0, 100));
    const code = query.get("code");
    if (!code) throw new LoginFailed(400, "pgmi.login.state_invalid", "the callback carries no code");

    const claims = await provider.verify(await provider.exchange(code, attempt.verifier), attempt.nonce);
    const token = await this.#withClient(pool, async (client) => {
      const { rows } = await client.query<{ token: string | null; problem: string | null }>(
        "SELECT token, problem FROM api.open_login_session($1, $2, $3, $4, $5, $6)", loginArguments(claims));
      if (rows[0].token === null) throw new LoginFailed(403, rows[0].problem ?? "membership.login.refused", "the database refused the sign-in");
      await this.#closePrevious(client, attempt.previous);
      return rows[0].token;
    });
    return { location: attempt.returnTo, cookies: [...this.#sessionCookies(token), clearCookie(LOGIN_COOKIE)] };
  }

  // NULL from the database: the issuer is not a development one.
  async devSignIn(cookies: Map<string, string>, subject: string, email: string, displayName: string | null, pool: pg.Pool): Promise<string[]> {
    const token = await this.#withClient(pool, async (client) => {
      const { rows } = await client.query<{ token: string | null }>(
        "SELECT api.open_dev_login_session($1, $2, $3, $4) AS token", [DEV_ISSUER, subject, email, displayName]);
      if (rows[0].token === null) throw new LoginFailed(403, "membership.login.refused", "the database refused the sign-in");
      await this.#closePrevious(client, cookies.get(SESSION_COOKIE));
      return rows[0].token;
    });
    return this.#sessionCookies(token);
  }

  async logout(cookies: Map<string, string>, pool: pg.Pool): Promise<string[]> {
    const session = cookies.get(SESSION_COOKIE);
    if (session) await this.#withClient(pool, (client) => client.query("SELECT api.close_login_session($1)", [session]));
    return [clearCookie(SESSION_COOKIE), clearCookie(CSRF_COOKIE)];
  }

  async #withClient<T>(pool: pg.Pool, use: (client: pg.PoolClient) => Promise<T>): Promise<T> {
    const client = await acquire(pool);
    try {
      return await use(client);
    } finally {
      client.release();
    }
  }

  async #closePrevious(client: pg.PoolClient, previous: string | undefined): Promise<void> {
    if (previous) await client.query("SELECT api.close_login_session($1)", [previous]);
  }

  #sessionCookies(token: string): string[] {
    return [setCookie(SESSION_COOKIE, token, SESSION_MAX_AGE), setCookie(CSRF_COOKIE, this.csrfToken(token), SESSION_MAX_AGE, false)];
  }
}
