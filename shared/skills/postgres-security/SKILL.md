---
name: postgres-security
description: PostgreSQL recipes for restricting which rows or functions a role can reach. Covers row-level security for tenant isolation (ENABLE and FORCE ROW LEVEL SECURITY, policies with USING and WITH CHECK, a tenant setting, who bypasses policies) and function privileges (the default EXECUTE grant to PUBLIC, REVOKE, ALTER DEFAULT PRIVILEGES, SECURITY DEFINER with a fixed search_path). Use when adding a multi-tenant table, writing a policy, creating a function or procedure that not every role may call, or reviewing grants.
---

# PostgreSQL security recipes

| Facing | Read |
|---|---|
| Tenants share tables and must never see each other's rows | [row-level security](references/row-level-security.md) |
| A new function or procedure, or a SECURITY DEFINER function | [function privileges](references/function-privileges.md) |

Read the project's `deploy.sql` before changing grants: it decides which roles
exist and how privileges are closed. A test of a grant or a policy must run as
a role that is not a superuser, or it proves nothing.
