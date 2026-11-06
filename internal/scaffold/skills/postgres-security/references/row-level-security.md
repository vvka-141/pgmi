# Isolate tenants with row-level security

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
Every table holds rows of many tenants, and every query must say
`WHERE tenant_id = ...`. One forgotten filter, in a report or an ad hoc fix,
returns or changes another tenant's rows. A row-level security policy makes
the database add the filter to every query, for every role it applies to.

## Use when
- Several tenants share tables and one role (the application's) reads them.
- The tenant is known per transaction and can be set as a setting.

## Don't use when
- The rule is which tables or functions a role reaches, not which rows: use
  `GRANT` (see function-privileges.md in this skill).
- Tenants need separate backups, extensions or schemas: use one database or
  schema per tenant.

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE shipment (
    shipment_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id bigint NOT NULL,
    amount numeric(12,2) NOT NULL
);
CREATE INDEX shipment_tenant ON shipment (tenant_id);

ALTER TABLE shipment ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment FORCE ROW LEVEL SECURITY;

CREATE POLICY shipment_tenant_isolation ON shipment
    USING (tenant_id = (SELECT NULLIF(current_setting('app.tenant_id', true), '')::bigint))
    WITH CHECK (tenant_id = (SELECT NULLIF(current_setting('app.tenant_id', true), '')::bigint));
```

Wrapped as `(SELECT ...)`, the setting is read once per query as an InitPlan,
not once per row. In the advanced template, call `core.apply_org_rls` instead:
it writes these policies, keyed on the request's organization.

The application sets the tenant once per transaction:
`SELECT set_config('app.tenant_id', '42', true);`. `USING` filters the rows a
command sees (`SELECT`, and the targets of `UPDATE` and `DELETE`). `WITH CHECK`
rejects rows a command writes (`INSERT`, and new versions from `UPDATE`) with
SQLSTATE `42501`. A policy without `WITH CHECK` reuses `USING` for both.

Superusers and `BYPASSRLS` roles always bypass policies. The table owner
bypasses them too, unless the table has `FORCE ROW LEVEL SECURITY`.

## Proof
Superusers bypass every policy, so the test creates its own role, as the
session user, and makes it the table owner: only `FORCE` subjects it to the
policy. The role rolls back with the test.

```sql runnable target=__test__/test_security_row_level_security.sql
DO $$
DECLARE
    v_count int;
BEGIN
    RESET ROLE;
    CREATE ROLE rls_probe_owner NOLOGIN;
    CREATE TEMP TABLE shipment (
        shipment_id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
        tenant_id bigint NOT NULL
    );
    INSERT INTO shipment (tenant_id) VALUES (1), (1), (2);
    ALTER TABLE shipment ENABLE ROW LEVEL SECURITY;
    ALTER TABLE shipment FORCE ROW LEVEL SECURITY;
    CREATE POLICY shipment_tenant_isolation ON shipment
        USING (tenant_id = (SELECT NULLIF(current_setting('app.tenant_id', true), '')::bigint))
        WITH CHECK (tenant_id = (SELECT NULLIF(current_setting('app.tenant_id', true), '')::bigint));
    ALTER TABLE shipment OWNER TO rls_probe_owner;

    SET LOCAL ROLE rls_probe_owner;
    PERFORM set_config('app.tenant_id', '', true);
    SELECT count(*) INTO v_count FROM shipment;
    IF v_count IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'no tenant set, yet % row(s) visible', v_count;
    END IF;

    PERFORM set_config('app.tenant_id', '1', true);
    INSERT INTO shipment (tenant_id) VALUES (1);
    SELECT count(*) INTO v_count FROM shipment;
    IF v_count IS DISTINCT FROM 3 THEN
        RAISE EXCEPTION 'tenant 1 sees % row(s), expected 3', v_count;
    END IF;

    BEGIN
        INSERT INTO shipment (tenant_id) VALUES (2);
        RAISE EXCEPTION 'tenant 1 inserted a row for tenant 2';
    EXCEPTION WHEN insufficient_privilege THEN
        NULL;
    END;
    RESET ROLE;
END $$;
```

## Pitfalls
- A transaction-local setting reads as `''`, not `NULL`, in later
  transactions of the same session. Without `NULLIF`, `''::bigint` fails every
  query with `22P02` instead of returning no rows.
- Connect the application as a role that is not a superuser and lacks
  `BYPASSRLS`. A test that queries as a superuser deploy role sees every row
  and proves nothing.
- A view runs with its owner's rights, so a view owned by the table owner
  skips the policy unless the table has `FORCE`. Create views over such tables
  `WITH (security_invoker = true)`.
- Unique and foreign-key checks bypass policies: a duplicate-key error on a
  global unique column tells one tenant another tenant's value exists. Make
  such keys unique per tenant: `UNIQUE (tenant_id, number)`.
- Index `tenant_id`, or lead composite indexes with it; the policy adds that
  predicate to every query.

## Source
- PostgreSQL documentation: Row Security Policies,
  https://www.postgresql.org/docs/current/ddl-rowsecurity.html; CREATE POLICY,
  https://www.postgresql.org/docs/current/sql-createpolicy.html; ALTER TABLE,
  https://www.postgresql.org/docs/current/sql-altertable.html.
- pgmi adaptation: the shipment schema, the empty-setting guard, and the test
  that proves the policy through a fresh non-superuser owner.
