---
title: "Hosting providers"
description: "What the advanced template does differently on Databricks Lakebase and Azure Database for PostgreSQL, how it detects them, and the two parameters that override it on any host."
weight: 178
---

# The advanced template on a managed host

A managed PostgreSQL service adds roles, schemas and limits of its own. The
advanced template holds grants to an exact list, so it has to know which of
those belong to the host. This page says what it does on each host it knows,
and how to tell it about one it does not.

pgmi itself knows no provider and has no provider flag. Everything here is
SQL in your project, in one file: `deploy/providers.sql`.

## The principle: detect, then default two parameters

The template looks at facts in the database, never at a name you pass. From
them it sets the default of two parameters (`pg_temp.provider_default`):

| Parameter | Meaning | Default |
|---|---|---|
| `provider_role_pattern` | The provider's own roles, as a `LIKE` pattern | `databricks\_%` on Lakebase, else empty |
| `provider_schemas` | The provider's own schemas, as a JSON array | `["__db_system"]` on Lakebase, else `[]` |

They say what the guards tolerate, not which host this is. Pass either one
to override the detected value, on any host:

```bash
pgmi deploy . -d myapp \
  --param 'provider_role_pattern=acme\_%' \
  --param 'provider_schemas=["acme_system"]'
```

With a pattern set, every deploy:

- revokes `CREATE` from the matching roles on the template's schemas and on
  `extensions` (`pg_temp.revoke_provider_create`). A role that can create in
  a schema on a `search_path` can shadow a function called there;
- revokes their `TRUNCATE`, `REFERENCES`, `TRIGGER` and `MAINTAIN` on the
  template's tables before the test gate
  (`pg_temp.revoke_provider_table_privileges`). These act on a whole table,
  past row-level security;
- lets the grant guards leave out the listed schemas and the matching roles'
  `USAGE` on schemas.
  [Grants a provider makes on its own](../PRODUCTION.md#grants-a-provider-makes-on-its-own)
  lists everything the guards ignore and everything they still refuse.

The override cannot switch the guards off for the template itself. A pattern
that matches one of the template's own roles is refused, and so is a
`provider_schemas` that is not a JSON array of names
(`deploy/parameters.sql`). A template schema named in `provider_schemas`
fails the test gate (`lib/__test__/test_approved_schemas.sql`).

## Databricks Lakebase

**Detected by** two marks together: the role `databricks_superuser` and the
schema `__db_system` (`pg_temp.on_lakebase()`). One alone does not count.

**What the host forces, and what the template does:**

- An event trigger grants Lakebase's roles `CREATE` on every new schema and
  whole-table privileges on every new table. The two defaults above apply,
  so each deploy revokes them.
- The deploy login's `REVOKE` on `public` removes nothing there, so the owner
  role revokes as well (`deploy/schemas.sql`).
- Lakebase refuses a weak password when the transaction commits. With
  `env=dev` the role passwords are therefore generated values, not
  `postgres`, and the deploy warns about it. Set
  `PGMI_PARAM_DATABASE_ADMIN_PASSWORD` to choose one.

**Override:** pass `provider_role_pattern` or `provider_schemas`.

Connecting, signing in and what Lakebase's reader role can see are in the
[Lakebase guide](../LAKEBASE.md#the-advanced-template-on-lakebase).

## Azure Database for PostgreSQL flexible server

**Detected by** the server setting `azure.extensions`.

**What the host forces, and what the template does:**

- Only extensions on that allowlist can be created. Before it changes
  anything, the deploy refuses when a required extension is missing from the
  list, and prints the `az postgres flexible-server parameter set` command
  that adds it (`pg_temp.refuse_disallowed_extensions`). The five are
  `uuid-ossp`, `pgcrypto`, `pg_trgm`, `hstore` and `btree_gist`.
- `azure_pg_admin` owns `public`, and the deploy login belongs to it, so the
  login's `REVOKE` on `public` is the one that counts
  (`deploy/bootstrap.sql`).

Azure sets no provider parameters: nothing there needs the guards relaxed.

**Override:** none needed. The extension check passes once the allowlist
holds the five extensions.

## Any other host

Amazon RDS and Aurora, Google Cloud SQL and AlloyDB, Neon and the rest are
not detected, and the template has not been run on them. Nothing in it is
written against them either: it needs PostgreSQL 18, a login with `CREATEDB`
and `CREATEROLE`, and the five extensions. A first deploy checks the login
before it changes anything and names what is missing.

If the deploy then fails a grant guard because of roles or schemas the host
adds, that is what the two parameters are for.

## Supporting a new provider

1. Deploy to a scratch database and read which roles and schemas the guards
   name.
2. Pass `provider_role_pattern` and `provider_schemas` for them. Keep the
   pattern narrow: the guards ignore grants to every role it matches.
3. If the host needs code (a check before the first change, a different
   default), put it in `deploy/providers.sql`, next to the two hosts already
   there. The file is yours after `pgmi init`.

The [hosting table](../PRODUCTION.md#managed-cloud-postgresql) says which hosts
have been run and how.
