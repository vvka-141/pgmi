// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project: it is a baseline to learn from,
// not a library. The rules it keeps are in ../../PROTOCOL.md ("MCP clients
// with OAuth", "Rate limits").
//
// The OAuth 2.1 resource server in front of /mcp, and the rate limits. An MCP
// client signs in at the identity provider and sends
// `Authorization: Bearer <access token>`. The token is checked here, against
// the provider's published keys, and the database gets only what it proved:
// issuer, subject and client id. The token itself is never forwarded and
// never logged. The database decides who that is and whether the client may
// act yet.
//
//   GET /.well-known/oauth-protected-resource[/mcp]   the metadata (RFC 9728)
//   POST /mcp without a valid token                    401 with
//       WWW-Authenticate: Bearer resource_metadata="..."

import { createRemoteJWKSet, customFetch, decodeJwt, decodeProtectedHeader, errors, jwtVerify } from "jose";
import type { JsonObject, McpContext } from "./channel.js";
import { checkedIssuerUrl, CLOCK_SKEW_SECONDS, TOKEN_ALGORITHMS } from "./login.js";

// Standard OAuth/OIDC token verification, not pgmi-specific: replace it with
// your own stack's verifier. The same three algorithms in every gateway.
const MAX_TOKEN_LENGTH = 8192;
const PROVIDER_TIMEOUT_MS = 5000;
// Unlike sign-in, these tokens come from any client: a token naming an unknown
// key refetches the key set at most once per this long per issuer, so a flood
// of forged key ids cannot flood the provider. A rotated key is picked up
// within this long.
const UNKNOWN_KEY_COOLDOWN_MS = 10_000;

// 401. error is "invalid_token", or null when no token was sent: RFC 6750
// §3.1 sends no error code to a request without credentials.
export class TokenRejected extends Error {
  constructor(readonly error: "invalid_token" | null, reason: string) {
    super(reason);
  }
}

// 503: the identity provider's discovery or keys could not be read.
export class ProviderUnavailable extends Error {}

export interface Identity { readonly issuer: string; readonly subject: string; readonly clientId: string }

// The MCP context api.mcp_handle_request resolves, and nothing else.
export function identityContext(identity: Identity): McpContext {
  return { client: { issuer: identity.issuer, subject: identity.subject, clientId: identity.clientId } };
}

// Scheme and host lower-cased, the rest as written: the MCP spec asks servers
// to accept an upper-case scheme or host in an audience.
export function canonical(uri: string): string {
  const match = /^([^:/?#]+):\/\/([^/?#]*)(.*)$/s.exec(uri);
  return match ? `${match[1].toLowerCase()}://${match[2].toLowerCase()}${match[3]}` : uri;
}

async function fetchOrUnavailable(url: string, init?: RequestInit): Promise<Response> {
  let response: Response;
  try {
    response = await fetch(url, init);
  } catch (err) {
    throw new ProviderUnavailable(`${url} could not be reached`, { cause: err });
  }
  if (response.status !== 200) throw new ProviderUnavailable(`${url} answered ${response.status}`);
  return response;
}

// One authorization server: discovery once, then its keys, cached. A failed
// discovery is not cached: the next request tries again.
class Issuer {
  #keys: Promise<ReturnType<typeof createRemoteJWKSet>> | undefined;

  constructor(readonly issuer: string) {}

  keys() {
    this.#keys ??= (async () => {
      const response = await fetchOrUnavailable(this.issuer.replace(/\/$/, "") + "/.well-known/openid-configuration",
                                                { signal: AbortSignal.timeout(PROVIDER_TIMEOUT_MS) });
      const metadata = await response.json().catch(() => null) as { issuer?: unknown; jwks_uri?: unknown } | null;
      if (metadata?.issuer !== this.issuer || typeof metadata.jwks_uri !== "string") {
        throw new ProviderUnavailable("discovery names another issuer or no jwks_uri");
      }
      return createRemoteJWKSet(new URL(metadata.jwks_uri), {
        cooldownDuration: UNKNOWN_KEY_COOLDOWN_MS, cacheMaxAge: 300_000, timeoutDuration: PROVIDER_TIMEOUT_MS,
        [customFetch]: fetchOrUnavailable,
      });
    })();
    this.#keys.catch(() => { this.#keys = undefined; });
    return this.#keys;
  }
}

export class ResourceServer {
  readonly #issuers: Map<string, Issuer>;
  readonly #refusedClients: ReadonlySet<string>;
  readonly metadataPaths: ReadonlySet<string>;
  readonly metadataUrl: string;

  constructor(readonly resource: string, authorizationServers: readonly string[], readonly scopes: readonly string[],
              loginClientId: string | undefined) {
    checkedIssuerUrl(resource, "MCP_RESOURCE");
    const url = new URL(resource);
    if (url.search || url.hash) throw new Error(`MCP_RESOURCE must have no query and no fragment: ${resource}`);
    if (authorizationServers.length === 0) throw new Error("MCP_RESOURCE is set but MCP_AUTHORIZATION_SERVERS is not");
    this.#issuers = new Map(authorizationServers.map((s) => [s, new Issuer(checkedIssuerUrl(s, "MCP_AUTHORIZATION_SERVERS"))]));
    // ADAPT: every client id that is not an MCP client. A token issued to the
    // web sign-in is a person's browser session, never an agent.
    this.#refusedClients = new Set(loginClientId ? [loginClientId] : []);
    const path = url.pathname.replace(/\/$/, "");
    this.metadataPaths = new Set(["/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource" + path]);
    this.metadataUrl = url.origin + "/.well-known/oauth-protected-resource" + path;
  }

  // undefined when MCP_RESOURCE is unset: /mcp then takes connection handles only.
  static fromEnvironment(env: NodeJS.ProcessEnv): ResourceServer | undefined {
    if (!env.MCP_RESOURCE) return undefined;
    const servers = (env.MCP_AUTHORIZATION_SERVERS ?? "").split(",").map((s) => s.trim()).filter(Boolean);
    const scopes = (env.MCP_SCOPES ?? "openid").split(/\s+/).filter(Boolean);
    return new ResourceServer(env.MCP_RESOURCE, servers, scopes, env.OIDC_CLIENT_ID || undefined);
  }

  // RFC 9728 §2. The MCP spec requires authorization_servers.
  metadata(): JsonObject {
    return {
      resource: this.resource,
      authorization_servers: [...this.#issuers.keys()],
      bearer_methods_supported: ["header"],
      scopes_supported: [...this.scopes],
    };
  }

  challenge(error: string | null): string {
    let value = `Bearer resource_metadata="${this.metadataUrl}"`;
    if (this.scopes.length) value += `, scope="${this.scopes.join(" ")}"`;
    if (error) value += `, error="${error}"`;
    return value;
  }

  // The identity a valid access token proves, or TokenRejected /
  // ProviderUnavailable. Cheap checks first: a token that is not a JWT, names
  // an issuer not configured or another audience is refused before any key
  // is fetched.
  async verify(authorization: string | undefined): Promise<Identity> {
    if (authorization === undefined) throw new TokenRejected(null, "no token");
    const [scheme, ...rest] = authorization.split(" ");
    const token = rest.join(" ").trim();
    if (scheme.toLowerCase() !== "bearer" || !token || token.length > MAX_TOKEN_LENGTH) {
      throw new TokenRejected("invalid_token", "not a bearer token");
    }
    let header, unverified;
    try {
      header = decodeProtectedHeader(token);
      unverified = decodeJwt(token);
    } catch {
      throw new TokenRejected("invalid_token", "not a JWT");
    }
    const issuer = typeof unverified.iss === "string" ? this.#issuers.get(unverified.iss) : undefined;
    if (issuer === undefined) throw new TokenRejected("invalid_token", "issuer not accepted");
    if (!TOKEN_ALGORITHMS.includes(header.alg ?? "") || typeof header.kid !== "string") {
      throw new TokenRejected("invalid_token", "algorithm or key id not accepted");
    }
    if (!this.#isAudience(unverified.aud)) throw new TokenRejected("invalid_token", "audience is not this resource");

    let claims;
    try {
      ({ payload: claims } = await jwtVerify(token, await issuer.keys(), {
        algorithms: TOKEN_ALGORITHMS, issuer: issuer.issuer, clockTolerance: CLOCK_SKEW_SECONDS,
        requiredClaims: ["iss", "sub", "aud", "exp", "iat"],
      }));
    } catch (err) {
      if (err instanceof errors.JWKSNoMatchingKey) throw new TokenRejected("invalid_token", "unknown key");
      if (err instanceof errors.JOSEError && !(err instanceof errors.JWKSTimeout)) {
        throw new TokenRejected("invalid_token", err.code);
      }
      if (err instanceof ProviderUnavailable) throw err;
      throw new ProviderUnavailable("key set unavailable", { cause: err });
    }
    if (typeof claims.iat !== "number" || claims.iat > Date.now() / 1000 + CLOCK_SKEW_SECONDS) {
      throw new TokenRejected("invalid_token", "iat is in the future");
    }
    // RFC 8707: the token was issued for this resource and only this one. A
    // token that also names another audience could be replayed here by that
    // other server.
    if (!this.#isAudience(claims.aud)) throw new TokenRejected("invalid_token", "audience is not this resource");
    const clientId = "client_id" in claims ? claims.client_id : claims.azp;
    if (typeof clientId !== "string" || !clientId || this.#refusedClients.has(clientId)) {
      throw new TokenRejected("invalid_token", "no MCP client id");
    }
    if (typeof claims.sub !== "string" || !claims.sub) throw new TokenRejected("invalid_token", "sub is not a string");
    return { issuer: issuer.issuer, subject: claims.sub, clientId };
  }

  #isAudience(audience: unknown): boolean {
    const one = Array.isArray(audience) && audience.length === 1 ? audience[0] : audience;
    return typeof one === "string" && canonical(one) === canonical(this.resource);
  }
}

// Standard web plumbing, not pgmi-specific: replace it with your framework's own.
// A token bucket per key, refilled continuously to perMinute a minute.
// Per process: N gateway processes allow N times the limit. One event loop,
// so no lock; memory is bounded by maxKeys, the least recently seen key going
// first (Map keeps insertion order; a hit moves the key to the end).
//
// ADAPT: behind a reverse proxy the client address is the proxy's; key on
// the address your proxy forwards, from a header only it can set, or limit at
// the proxy and set RATE_LIMIT_PER_IP=0 here.
export class RateLimiter {
  readonly #buckets = new Map<string, { tokens: number; seen: number }>();

  constructor(readonly perMinute: number, private readonly maxKeys = 10_000,
              private readonly clock: () => number = () => performance.now() / 1000) {}

  // [true, 0], or [false, seconds until the next request is allowed].
  allow(key: string): [boolean, number] {
    if (this.perMinute <= 0) return [true, 0];
    const rate = this.perMinute / 60;
    const now = this.clock();
    const bucket = this.#buckets.get(key) ?? { tokens: this.perMinute, seen: now };
    this.#buckets.delete(key);
    let tokens = Math.min(this.perMinute, bucket.tokens + (now - bucket.seen) * rate);
    const allowed = tokens >= 1;
    if (allowed) tokens -= 1;
    this.#buckets.set(key, { tokens, seen: now });
    while (this.#buckets.size > this.maxKeys) this.#buckets.delete(this.#buckets.keys().next().value!);
    return allowed ? [true, 0] : [false, Math.max(1, Math.ceil((1 - tokens) / rate))];
  }
}
