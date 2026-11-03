# Identity and access

The README's sections "Sign-in, sessions and connections", "Organizations
and members", "Role Hierarchy" and "Privileges: closed by default" are the
reference. This page is the part a handler author needs.

## Contents

1. Who is calling
2. May they call this handler
3. Which rows they see
4. Roles and kernels
5. Testing as someone

## 1. Who is calling

- The gateway signs the person in (OIDC) and calls
  `api.open_login_session(iss, sub, email, name, email_verified, amr)`, the
  only path that creates a person. It keeps the returned handle in an
  `HttpOnly` cookie and sends it as `x-login-session`.
- A tab opens an organization session (`POST /organization-sessions`) and
  sends `x-organization-session`: one tab, one organization.
- An MCP client sends a connection handle the person approved on the web
  (`POST /connections` with `organizationId` and an agent `roleId`), fixed to
  that organization, or an OAuth access token the gateway verifies (audience
  exactly the `/mcp` URI). Only the token's issuer, subject and client id
  reach the database (`{"client": ...}` in the MCP context); an unknown
  (person, client) becomes a pending connection whose pairing code only
  that person approves (`POST /connections/approve`). One live connection
  per person and client. A client that cannot act yet answers `-31001` with
  `data.code` (`membership.connection.pending`, `.paused`, `.disabled`,
  `.not_admitted`). Approval creates the connection's agent principal. The
  request acts for the person (the effective principal: row-level security
  and `api.vw_current_user` see them) and is made by the agent.
- An agent's permissions are its `agent.*` role's (`data/authorization.json`)
  intersected with the person's in that organization, recomputed per
  request; never a platform permission. Approval refuses a role broader than
  the person (`membership.connection.role_too_broad`), and only a person acting as
  themselves approves, pauses, resumes or revokes: an agent never changes an
  agent. Agent roles are never assigned to members.
- A connection acts only while live: not paused (by its person under
  `/connections`, or by a `members.manage` holder under `/agents`, whose
  pause only a manager lifts), not revoked or expired, its membership
  current, and its organization's agents not switched off by the owner
  (`POST /agents/disable`). Ending a membership revokes the person's
  connections into it. Each change is audited with the agent as subject.
- Each request resolves the handles into a request context that only a login
  in the api role may state. Handlers read it through
  `api.current_principal_id()`, `api.current_organization_id()`,
  `api.current_permissions()`, `api.has_permission(text)`,
  `api.is_authenticated()` and `api.vw_current_user`. The same readers serve
  REST, RPC and MCP.
- Who really acts is `api.current_actual_principal_id()` with
  `api.current_actor_kind()`: `human` (a person signed in), `agent` (an MCP
  connection), `service`, or `system` (no principal: the deploy, a sign-in).
  Record it on anything that says who did something (`*_by_object_id`
  columns); the audit log, change tracking, the access-denial log and the
  exchange log already do. Decide access on the effective principal, never
  on the actual one.

## 2. May they call this handler

One function decides for every protocol and for the MCP listings, before the
body runs, from the handler's declaration:

| Declared | Refused with |
|---|---|
| `access: public` | never |
| `access: authenticated` | 401 when no person resolves |
| `permissions: [...]` | 401; 403 `pgmi.organization_required` without an organization session; 403 `pgmi.forbidden` without one of the permissions |

Every refusal is logged in `api.access_denial`. Permissions and roles are
`data/authorization.json`, synced on every deploy. A new permission-guarded
REST handler needs a row in `api/__test__/test_access_matrix.sql`; change
that table in the same commit as a role's grants. `membership.fn_simulate_access`
and the `GET /access/*` endpoints explain who may call what, and why.

Object-level rules (only the author may edit a comment) are the body's: a
coded 403, or a row policy.

## 3. Which rows they see

- Every table in an application schema is `ENABLE` + `FORCE ROW LEVEL
  SECURITY` with a policy, or exempt:
  `SELECT pg_temp.exempt_from_rls('core.country', 'reference data synced by the deploy');`.
  `lib/__test__/test_forced_rls.sql` names any other.
- `core.apply_org_rls('core.<table>')` installs the four policies keyed on
  `organization_id = (SELECT api.current_organization_id())`. Wrap an
  identity reader in `(SELECT ...)` in any policy you write, or
  `test_rls_initplan.sql` fails: unwrapped, it runs once per row.
- Handlers run as the owner, which FORCE holds to the policies too. A row
  another organization owns is invisible, so the handler answers 404.
- A deploy-time backfill runs through
  `core.execute_with_force_rls_lifted(ARRAY['core.task']::regclass[], $sql$...$sql$)`.

## 4. Roles and kernels

| Role | Is |
|---|---|
| owner (NOLOGIN) | owns every object; every handler runs as it |
| kernel (NOLOGIN) | owns the `SECURITY DEFINER` kernels that work across organizations |
| api (NOLOGIN) | the gateway's bundle: its entry points, nothing else |
| admin (LOGIN) | owner + api, for operators |

- No routine is executable by `PUBLIC`; the deploy revokes any such grant,
  and `test_exact_grants.sql` fails on a grant outside each role's list.
- A kernel that writes another organization's rows, or membership rows, is
  `SECURITY DEFINER` with a fixed `search_path`, handed over right after its
  `CREATE` with `SELECT pg_temp.kernel_owns('core.fn_x(uuid)')`, and passes
  forced tables by `pg_temp.kernel_passes('core.task', '{insert,update}')`.
  Because it sees every organization, it checks its own reach
  (`membership.fn_is_organization_in_reach`,
  `membership.fn_is_principal_in_reach`).
- A kernel that only touches the request's organization needs none of this:
  a plain `LANGUAGE sql` function, run as the owner under row-level security.

## 5. Testing as someone

`api/__test__/_setup.sql` signs up Alice (owner of Acme Corp) and Bob (member)
and sets `test.alice_subject`, `test.bob_subject`, `test.acme_org_id`. The
helpers are `pg_temp` functions from `membership/09-test-sessions.sql`:

| Helper | Gives |
|---|---|
| `pg_temp.test_session_headers(login, organization)` | REST/RPC headers for a login session, plus an organization session when given |
| `pg_temp.test_mcp_context(login, organization, role)` | the MCP context of an approved connection; without a role, the agent role covering what the person holds |
| `pg_temp.test_act_as(login, organization, mfa)` | a request context stated directly; `NULL` clears it |
| `pg_temp.test_login(issuer, subject, email, name, verified)` | a new person, signed up as a sign-in would |
| `pg_temp.test_as_kernel()` / `pg_temp.test_as_owner()` | switch role to read or plant membership rows |

A test of a grant or a policy proves nothing when it runs as a superuser.
