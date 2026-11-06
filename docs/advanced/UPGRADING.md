---
title: "Upgrading an advanced-template project"
description: "Take template fixes into a project you have changed: a three-way merge against the template you started from, and what moving from v0.13 to v0.14 takes."
weight: 180
---

# Upgrading an advanced-template project

`pgmi init` copies the template into your project once. From then on the files
are yours: a new pgmi binary does not change them, and a template fix,
security fixes included, reaches your project only when you take it.

Comparing your project with a fresh scaffold of the new version is not enough.
That diff cannot tell your own edits from the template's changes, because it
has no common starting point. Merge three ways instead: the template you
started from (the **base**), the template you are moving to, and your project.

## 1. Find the base version

Projects scaffolded by pgmi v0.13.0 or later name it on the first line of
`deploy.sql`, and `pgmi info` prints it:

```sql
-- Scaffolded by pgmi 0.13.0 from the advanced template.
```

Older projects carry no such line. Take the version from your git history (the
commit that added the scaffold, or the pgmi version your team used then). If
you have already taken a template upgrade, the base is the version you
upgraded to last.

## 2. Scaffold the base and the target

`pgmi init` writes only the templates built into the running binary, so the
base needs the base version's binary. Install it next to your current one:

```bash
GOBIN=/tmp/pgmi-base go install github.com/vvka-141/pgmi/cmd/pgmi@v0.13.0
```

Or take the binary from that release's archive on GitHub. Then scaffold both
templates. `pgmi init` names the project after its directory, so give both the
name of your project's directory (here `myapp`) and the files they write match
yours:

```bash
/tmp/pgmi-base/pgmi init /tmp/base/myapp   --template advanced   # the version you started from
pgmi                init /tmp/target/myapp --template advanced   # the version you are moving to
```

## 3. Merge with git

Commit your project first, so the merge is the only change. Then put both
templates on a scratch branch and apply the difference between them to your
project:

```bash
git switch --orphan pgmi-template
cp -r /tmp/base/myapp/. .
git add -A && git commit -m "template: advanced, pgmi 0.13.0"
git rm -rq . && cp -r /tmp/target/myapp/. .
git add -A && git commit -m "template: advanced, pgmi 0.14.0"

git switch main
git cherry-pick pgmi-template
```

`cherry-pick` applies what changed between the two template commits to your
files, three ways, with the base as the common ancestor. Files the template
added appear, files it removed go, and files it changed are merged with your
edits. Where you and the template changed the same lines, git stops with a
conflict. Resolve it as usual (`git status`, edit, `git add`,
`git cherry-pick --continue`). Then delete the scratch branch with
`git branch -D pgmi-template`.

Prefer this to per-file `git merge-file`: it also handles the files a release
adds, removes or renames.

## 4. Read the release's Upgrading notes

Every release that changes the template lists what a merge alone cannot do in
its **Upgrading** section of [`RELEASES.md`](../../RELEASES.md): a parameter
that went away, data to review, a step to run once. Do those before you deploy.

## From v0.13 to v0.14: a new database

v0.14 rebuilds the advanced template's structure (run-once structure files, a
new membership model, PostgreSQL 18), so **it cannot be deployed onto a
database created by v0.13 or earlier**. Merge your project as above, then
deploy it to a new database and move your data across. pgmi ships no in-place
upgrade before 1.0.

What you will meet (checked with real projects built by v0.13.0 and v0.13.1):

1. **Your own files must follow the file contract.** v0.13 ran every file on
   every deploy, so a file could hold a table and its functions together.
   v0.14 refuses that:

   ```
   ERROR: Re-run file(s) define structure: ./core/note.sql (CREATE TABLE)
   HINT: Move tables, indexes, types, domains, sequences, policies, operators, constraint triggers and ALTER TABLE into a file with idempotent="false".
   ```

   Split each such file: tables, indexes, types and policies go in a run-once
   file (`idempotent="false"`, plain `CREATE TABLE`, no `IF NOT EXISTS`) sorted
   before the code; functions and views stay in the re-run file. See the
   template README's file contract.

2. **Your REST routes must be path templates.** A `uri` regex, an `example`
   or a `version` key is refused at registration. Rewrite each route as a
   `path` template, one parameter per whole segment, and read the values from
   `(request).path_params` instead of parsing `(request).url`:

   ```sql
   -- v0.13: 'uri', '^/projects/([0-9a-f-]+)$'
   'path', '/projects/{id:uuid}'
   -- in the handler
   v_id := ((request).path_params->>'id')::uuid;
   ```

   A literal segment now outranks a parameter, so `/projects/stats` and
   `/projects/{id}` coexist. Version in the path (`/v2/projects`), not in a
   header. `api.rest_route_policy` lost its version argument and
   `api.rest_route_policy_for` is gone: a gateway calls
   `api.rest_route_policy(method, url, requested_isolation)`.

3. **Your handlers must declare access, not `requiresAuth`.** Every REST, RPC
   and MCP registration declares exactly one of `access` or `permissions`; a
   `requiresAuth` key is refused at registration:

   ```
   ERROR: Handler list_projects declares "requiresAuth", which is not a handler key
   ```

   Translate each registration:

   ```sql
   -- v0.13: 'requiresAuth', false
   'access', 'public'
   -- v0.13: 'requiresAuth', true
   'access', 'authenticated'              -- any signed-in caller
   'permissions', jsonb_build_array('project.read')  -- or: callers holding one of these
   ```

   Prefer `permissions` for an operation on an organization's data: name the
   permission it needs from `data/authorization.json`, and add the permission
   there (and to the roles that should hold it) if it is new. Keep
   `authenticated` for routes any signed-in caller may use, such as reading
   their own profile.

4. **MCP clients connect as agents of an approved connection.** v0.13's
   gateway trusted an `X-User-Id` header (and `X-Tenant-Id`) and passed it
   to the database. v0.14 ignores both. A person signs in on the web, then
   approves each client, naming the organization it works in and an agent
   role:

   ```bash
   curl -X POST http://localhost:8080/api/connections \
     -H "Content-Type: application/json" -H "X-CSRF-Token: ..." --cookie "__Host-session=..." \
     -d '{"clientName": "Desktop assistant", "organizationId": "<uuid>", "roleId": "agent.contributor", "validDays": 90}'
   ```

   The answer carries a `cn_` handle once; the client sends it as
   `X-Connection`. Or set `MCP_RESOURCE` and `MCP_AUTHORIZATION_SERVERS` on
   the gateway, and the client signs in with OAuth: its first call answers a
   pairing code, which the person approves with
   `POST /api/connections/approve` (`pairingCode`, `organizationId`,
   `roleId`). Either way the agent has its own principal and holds its
   `agent.*` role's permissions intersected with the person's. With
   `MCP_RESOURCE` set, an anonymous `/mcp` call is 401. See
   [Run the MCP gateway](MCP-GATEWAY.md).

   If you added permissions to `data/authorization.json`, decide which
   `agent.*` roles grant them too. An agent holds only what both its role and
   its person hold, so a permission no agent role grants is out of every
   agent's reach except `agent.delegate`'s.

5. **The gateway moved, and its settings were renamed.**
   `tools/mcp-gateway.py` is now `gateways/python/gateway.py`, beside
   reference gateways in TypeScript, Go, C# and Rust that serve the same
   surface. Every gateway reads the same environment variables
   (`gateways/PROTOCOL.md`, "Configuration"):

   | v0.13 | v0.14 |
   |---|---|
   | `MCP_ALLOWED_ORIGINS` | `ALLOWED_ORIGINS` (now applied to every endpoint) |
   | `MCP_MAX_RETRY_ATTEMPTS` | `RETRY_MAX_ATTEMPTS` |
   | `MCP_RETRY_BASE_DELAY` | `RETRY_BASE_DELAY` |
   | `MCP_RETRY_MAX_DELAY`, `MCP_RETRY_AFTER` | removed: the backoff is capped at 1 s, `Retry-After` is 1 |

   New: `POOL_MAX_SIZE`; `OIDC_*`, `LOGIN_CSRF_SECRET` and `DEV_LOGIN` for
   sign-in; `MCP_RESOURCE`, `MCP_AUTHORIZATION_SERVERS` and `MCP_SCOPES` for
   OAuth on `/mcp`; `RATE_LIMIT_PER_IP` and `RATE_LIMIT_PER_CLIENT`;
   `INTERCEPTORS` and `EMBED_FAILURE_POLICY` for the example interceptors.
   The gateway now serves REST under `/api`; the new deploy parameter
   `api_base_path` (default `/api`) names that mount in the OpenAPI
   document, so set it if your gateway serves REST elsewhere. The gateway
   connects as a login in the api role (`<db>_api`), never as the owner.

6. **Branch on the new problem codes.** Application refusals take the form
   `<schema>.<entity>.<reason>`, such as `membership.connection.role_too_broad`
   or `membership.connection.pending`; framework ones stay `pgmi.*`. A client
   that matched v0.13's messages or codes matches these instead, and never
   the `title` or `detail`.

7. **The old database is refused.** Deployed onto a database built by v0.13
   or earlier, the merged project stops before it touches anything:

   ```
   ERROR: Database myapp was built by pgmi v0.13 or earlier; this template cannot deploy onto it (SQLSTATE 55000)
   DETAIL: v0.14 rebuilt the template's structure and membership model. Nothing was changed.
   HINT: Deploy to a new database and move the data across: see docs/advanced/UPGRADING.md, "From v0.13 to v0.14" (https://github.com/vvka-141/pgmi/blob/main/docs/advanced/UPGRADING.md).
   ```

   This is expected. Deploy to a new database instead (PostgreSQL 18):

   ```bash
   pgmi deploy . -d myapp_v14 --param env=test --params-file secrets.env
   ```

8. **Move the data.** Tables whose shape did not change copy directly, one
   table at a time, data only:

   ```bash
   pg_dump --data-only --table=core.note myapp | psql -d myapp_v14
   ```

   Identity columns keep their values, and `pg_dump` advances their
   sequences. Membership data does not copy table for table, because v0.14
   replaced the model: map people, organizations and roles into the new
   tables with `INSERT … SELECT` over a `postgres_fdw` or `dblink` connection
   to the old database, and check the result with your own tests before you
   switch traffic.

9. **Switch over** when the new database's tests and your checks pass, then
   retire the old one.

## Security releases

A fix to the template is delivered as a pgmi release with a
[GitHub security advisory](https://github.com/vvka-141/pgmi/security/advisories).
The advisory names the template files involved and how to tell whether your
project is affected. To hear about them, watch the repository for security
alerts (Watch → Custom → Security alerts). Then take the release into your
project with the merge above, and follow its Upgrading notes.
