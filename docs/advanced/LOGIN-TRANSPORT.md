---
title: "Login transport contract"
description: "What the web server in front of an advanced-template database must do: OIDC sign-in, the session cookie, the session and connection headers, logout, and the roles it connects as."
weight: 35
---

# Login transport contract

> **Scope: advanced template only.** This is the contract of the SQL that
> `pgmi init --template advanced` copied into your project.

The database never signs anyone in. A transport does: the web server or
gateway in front of PostgreSQL. It runs the OpenID Connect sign-in, hands the
result to the database once, and from then on passes opaque handles. The
database decides who the caller is, which organization the request acts in,
and what it may do. No identity, email or organization is ever taken from a
header.

The template ships a reference transport in each of its five gateways
(Python, TypeScript, Go, C# and Rust, under `gateways/`;
[below](#the-reference-transport)). For local work without an identity
provider, dev impersonation (below) opens sessions directly.

## The handles

| Handle | Prefix | Issued by | Carried | Scope |
|---|---|---|---|---|
| Login session | `ls_` | `api.open_login_session` | the transport's httpOnly cookie, then the `x-login-session` header to the database | one person, one sign-in |
| Organization session | `os_` | `POST /organization-sessions` | the browser tab, as the `x-organization-session` header | one membership, inside one login session |
| Connection | `cn_` | `POST /connections` | the MCP client, as the `X-Connection` header | one agent of one person, in one organization, for one MCP client |

Each handle is 32 random bytes. The database stores only its SHA-256, so a
database dump holds no usable handle. Every handle is a bearer secret: serve
the API over TLS only.

## Sign-in

1. Run the OIDC authorization code flow with PKCE against an issuer listed in
   the `oidc_issuers` deploy parameter. Check the ID token: signature, `iss`,
   `aud`, `exp` and `nonce`, and the `state` of the callback.
2. Call, as the transport's database login:

   ```sql
   SELECT token, problem
   FROM api.open_login_session(
       p_issuer_id         => :iss,             -- the iss claim, exactly as listed in oidc_issuers
       p_subject           => :sub,             -- the sub claim; may contain '|', never whitespace
       p_email             => :email,
       p_display_name      => :name,
       p_is_email_verified => :email_verified,  -- the email_verified claim, as the provider sent it
       p_amr               => :amr);            -- the amr claim, as a text[]
   ```

   It returns a handle in `token`, or a code in `problem`:

   | `problem` | Meaning |
   |---|---|
   | `membership.login.issuer_unknown` | the issuer is not in `oidc_issuers` (development issuers are refused here too) |
   | `membership.login.subject_invalid` | the subject is empty or contains whitespace |
   | `membership.login.email_missing` | a first sign-in without a well-formed email |
   | `membership.login.email_unverified` | the email belongs to an existing person, and the new login's email or that person's email is not verified |
   | `membership.login.principal_disabled` | the person is disabled |
   | `membership.login.refused` | any other refusal |

3. Store the handle in a cookie: `HttpOnly`, `Secure`, `SameSite=Strict`,
   `Path=/`, no `Domain`. Strict keeps it off every cross-site request, so a
   link on another site cannot make a signed-in GET. Page scripts never see it. Each sign-in
   gets a new handle; never reuse a handle that existed before the sign-in.

What `api.open_login_session` does is the only way a person enters the
database. A new login becomes a new person. It is linked to an existing person
with the same email only when both emails are verified: the new login's
(`email_verified`) and the existing person's. It accepts the person's pending
invitations, which need a verified email that matches. It creates no
organization.

**Pass `email_verified` and `amr` through unchanged.** `email_verified` decides
account linking and invitations. `amr` decides the second factor: a session
counts as MFA when `amr` contains `mfa`, and platform permissions (such as
`platform.operate`) need it while `platform_mfa_required` is `true`, the
default. If your provider reports a second factor with other RFC 8176 values
(`otp`, `hwk`), map them to `mfa` in the transport, after checking them.

A login session expires after 12 hours idle or 7 days after sign-in. Idle
expiry slides, at most once every 5 minutes, and never inside a read-only
transaction.

## Every request

Call the gateway functions (`api.rest_invoke`, `api.rpc_invoke`,
`api.mcp_handle_request`) with the request headers, after these rules:

- **`x-login-session`** is written by the transport, from its cookie. A
  client-sent `x-login-session`, in any letter case, is dropped. A request
  without the cookie carries no login session and is anonymous.
- **`x-organization-session`** comes from the browser tab and is forwarded as
  sent. It is valid only together with the login session it was opened in, so
  on its own it is worth nothing.
- **Never forward a session or connection header the transport did not
  issue.** Anything else a client sends under these names, in any case, is
  dropped. The database checks this too: when two header names differ only in
  case, REST and RPC answer 400 instead of guessing which one is meant.
- A request carries at most one organization. Signing in does not choose one.

Responses name both session headers in `Vary`, and a 401 carries
`WWW-Authenticate: Session` (the deploy parameter `www_authenticate` changes it). Copy every response header the database returns.

## Organization sessions

A tab picks the organization it works in:

```http
POST /organization-sessions
Content-Type: application/json

{"organizationId": "0196f0a4-..."}
```

The answer is 200 with `{"organizationSession": "os_...", "organizationId": "..."}`,
422 naming `organizationId` when the person is not a current member, or 409 when
the organization is not active. The tab keeps the handle in memory (or `sessionStorage`) and sends it as
`x-organization-session`. To switch organization, open another one.
`DELETE /organization-sessions/current` ends the tab's session. An
organization session ends with its login session, and when the membership ends.

## Logout

Call `api.close_login_session(handle)` with the cookie's handle, then clear the
cookie. It revokes the login session, and every organization session opened in
it.

## MCP clients: connections

An MCP caller is the agent of a connection a person approved. Approval
creates the agent's own principal: requests through the connection act for
the person (the effective principal, which row-level security sees) and are
made by the agent (the actual principal, actor kind `agent`). Signed in on
the web, the person calls `POST /connections` with
`{"clientName": "...", "organizationId": "...", "roleId": "agent.reader", "validDays": 90}`
and gets a `cn_` handle, shown once. The agent holds the role's permissions
intersected with the person's. The MCP client sends it as `X-Connection`.
The gateway puts it into the MCP context as `{"connection": "..."}` and
forwards nothing else. The organization is the one the connection was
approved in; an organization the client sends is ignored. Nothing on MCP
provisions anyone, and a connection never holds a platform permission. See
[Run the MCP gateway](MCP-GATEWAY.md).

An MCP client can also sign in itself, at the same identity provider, for
an access token whose audience is the gateway's `/mcp` (`MCP_RESOURCE`). The
gateway verifies the token and puts only `{"client": {"issuer", "subject",
"clientId"}}` into the MCP context. The person is the one that issuer and
subject signed in as on the web; a token issued to the web sign-in's own
client (`OIDC_CLIENT_ID`) is refused, so a browser session never becomes an
agent. The first call answers a pairing code, which the person approves with
`POST /connections/approve`; the connection that results has no handle.

## The database login

The transport connects as a login that is a member of the api role
(`database_api_role`, default `<db>_api`). That membership is the trust gate:
the database reads the request context only from a login in the api role, and
ignores it from any other. The api role executes exactly the gateway entry
points, `api.open_login_session` and `api.close_login_session` among them.
Grant it only to the transport's login.

## The reference transport

Each of the template's five gateways implements this contract, in its own
file:

| Gateway | Sign-in rules in | Token verification |
|---|---|---|
| Python | `gateways/python/login_transport.py` | PyJWT (`PyJWT[crypto]`) |
| TypeScript | `gateways/typescript/src/login.ts` | `jose` |
| Go | `gateways/go/login.go` | go-oidc, `golang.org/x/oauth2` |
| C# | `gateways/csharp/Gateway/SignIn.cs` | `Microsoft.IdentityModel` |
| Rust | `gateways/rust/src/login.rs` | `jsonwebtoken` |

Each serves, beside `/mcp`:

| Endpoint | Does |
|---|---|
| `GET /auth/login?returnTo=/path` | starts the authorization code flow with PKCE (S256), a fresh `state` and `nonce`, and a short-lived `__Host-login` cookie that binds the flow to this browser |
| `GET /auth/callback` | accepts the callback once, only in the browser that started it and only with its `state`; exchanges the code with the verifier; verifies the ID token (signature through the issuer's JWKS, `iss`, `aud`, `exp`, `iat`, `nonce`, and `azp` when `aud` lists several clients); calls `api.open_login_session`; closes the handle the browser held at `/auth/login` (kept with the pending sign-in, since the `SameSite=Strict` session cookie does not travel on the provider's cross-site redirect); redirects to `returnTo` (same-site paths only); a missing or refused `returnTo` lands on the API explorer at `/api/docs` |
| `POST /auth/logout` | calls `api.close_login_session` and clears the cookies |
| any method on `/api/<path>` | `api.rest_invoke` for `/<path>`, with the transaction policy resolved first and serialization failures retried, as on `/mcp` |

Configure it with environment variables:

| Variable | Meaning |
|---|---|
| `OIDC_ISSUER` | the issuer, exactly as listed in `oidc_issuers`; `https`, or `http` on a loopback host. Unset, `/auth/` answers 404 and `/api/` is anonymous |
| `OIDC_CLIENT_ID` | the client registered with the issuer |
| `OIDC_CLIENT_SECRET` | for a confidential client (sent as HTTP Basic); leave it out for a public one |
| `OIDC_REDIRECT_URI` | this gateway's `/auth/callback`, as registered |
| `OIDC_SCOPES` | default `openid email profile` |
| `LOGIN_CSRF_SECRET` | the key CSRF tokens are derived with; set the same value on every gateway process (default: random per process) |
| `ALLOWED_ORIGINS` | the Origin allowlist, applied to every endpoint |

The cookies:

- `__Host-session`: the `ls_` handle; `HttpOnly`, `Secure`, `SameSite=Strict`,
  `Path=/`, no `Domain`, 7 days. The `__Host-` prefix stops a sibling
  subdomain from planting one.
- `__Host-csrf`: the CSRF token, readable by page scripts. A request with the
  session cookie and a method other than GET, HEAD or OPTIONS must send it in
  `X-CSRF-Token`, or it gets 403 `pgmi.csrf_refused`. The token is an HMAC of
  the session handle, so it changes with every sign-in.

Per request on `/api/`, the gateway writes `x-login-session` from the cookie,
drops a client-sent `X-Login-Session`, `X-Connection` and `Cookie` in any
letter case, forwards `x-organization-session` as sent, joins a repeated
header name, refuses a repeated single-valued one such as
`X-Organization-Session` in any letter case (400 `pgmi.header_collision`;
the list is in the template's `gateways/PROTOCOL.md`) and an Origin not
on the allowlist (403 `pgmi.origin_refused`). Sign-in failures answer
`application/problem+json` with a `code`: `pgmi.login.state_invalid` (400),
`pgmi.login.token_invalid` (401), the database's `login.*` code (403), or
`pgmi.login.provider_unavailable` (502).

`email_verified` and `amr` reach `api.open_login_session` as the provider sent
them. A value of the wrong JSON type (`"true"` as a string, `amr` that is not
an array of strings) becomes NULL, which the database reads as unverified and
as no second factor. Map provider-specific second-factor values to `mfa` in
`login_arguments` if your provider needs it.

**Dependency.** Each gateway checks ID token signatures with one pinned
library (the table above): a reference others copy must not hand-roll
public-key signature verification. The flow itself (state, nonce, PKCE,
`returnTo`, the cookies, the problem codes) is written by hand, the same in
every language. What a library leaves to the caller (the nonce, `azp` with
several audiences, `iat`, the leeway) each gateway checks itself.

**What a production deployment adds.** TLS in front of the gateway (the
cookies are `Secure`). Several gateway processes need a shared
`LOGIN_CSRF_SECRET`, and sticky routing between `/auth/login` and
`/auth/callback`, because pending sign-ins are held in process memory for 10
minutes. A rate limit on `/auth/`. A Content-Security-Policy on the pages you
serve, since any script on your origin can read `__Host-csrf`. Refresh of the
provider's discovery document if it can change while the gateway runs.

**Tests.** The end-to-end harness in `internal/scaffold/testdata/gateways/e2e/`
deploys the template (`env=dev`) into PostgreSQL 18, runs a mock OIDC provider
and a reference gateway, and checks the happy path and every refusal above:
state, replay and cross-browser callbacks, the provider's `error=`, PKCE,
nonce, audience (several audiences need `azp`), issuer, expiry, a missing
`iat`, signature, `alg` `none`, HS256 keyed with the public key, key rotation,
discovery naming another issuer, an unreachable provider, unverified email
linking, `amr` and platform permissions, cookie flags, CSRF and Origin,
fixation, logout, client-sent session headers and header case collisions. CI
runs it against every reference gateway in the examples workflow.

## Dev impersonation

With `env=dev`, `api/dev/impersonation.sql` adds

```sql
SELECT api.open_dev_login_session('dev', 'alice', 'alice@example.com', 'Alice');
```

It opens a login session for a development issuer only (`dev`, seeded by
`membership/dev/01-issuers.sql`), counted as verified with `amr` `[mfa]`. It is
not deployed with `env=test` or `env=prod`, and `api.open_login_session` refuses
development issuers. A local gateway can sign in with it and set the cookie as a
real transport would.

The reference gateways in `gateways/` do exactly that on `POST /auth/dev-login`
(`{"subject", "email", "displayName"}`), only when started with
`DEV_LOGIN=1`. Their tests sign in this way.

## Organizations

Organizations are invitation-only. A platform operator creates one with
`POST /admin/organizations` (`{name, slug, ownerEmail}`). It starts pending and
becomes active when the invited owner signs in with that verified email. The
one self-serve path is `api/dev/self-serve-organizations.sql`:
`POST /organizations` (`{name, slug}`) calls
`membership.fn_create_organization_with_owner` and makes the caller the owner.
It deploys with `env=dev` only; move the file into `api/` to offer it
everywhere.

## Roles

| Role | Login | Purpose |
|---|---|---|
| `database_owner_role` (`<db>_owner`) | no | owns every object; every handler runs as it, held to forced row-level security |
| `database_kernel_role` (`<db>_kernel`) | no | owns the `SECURITY DEFINER` membership kernels that work across organizations; each checks its own reach; never granted to a login |
| `database_api_role` (`<db>_api`) | no | the transport's bundle: the gateway entry points and nothing else |
| `database_admin_role` (`<db>_admin`) | yes | operators and migrations; a member of the owner and the api role |
| `database_jobs_role` (`<db>_jobs`) | yes | optional, never created by the deploy: a scheduler's login that runs `api.job_*` routines and nothing else, granted only once registered as a service principal |

The deploy role needs `CREATEROLE` and `CREATEDB`, not superuser: `deploy.sql`
grants itself the roles it creates.
