/*
<pgmi-meta
    id="85c16de9-a1cc-491b-88e6-4db887f684c8"
    idempotent="true">
  <description>
    Core foundation: entity_id domain type. Tables that declare object_id as
    core.entity_id opt into lifecycle standards applied by the deploy-end
    sweep in entity-standards.sql (created_at, deleted_at columns).
  </description>
  <sortKeys>
    <key>003/000</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing core entity foundation'; END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_type
        WHERE typname = 'entity_id' AND typnamespace = 'core'::regnamespace
    ) THEN
        CREATE DOMAIN core.entity_id AS uuid;
    END IF;
END $$;

COMMENT ON DOMAIN core.entity_id IS
    'Opt-in marker for entity lifecycle standards. Declare a column "object_id core.entity_id" in your CREATE TABLE and the deploy-end sweep injects created_at and deleted_at columns automatically. Call pg_temp.apply_entity_table_standards(regclass) inline if you need the columns immediately for indexes. Works with plain and partitioned tables alike.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ core.entity_id - domain marker for entity tables';
END $$;

-- ============================================================================
-- core.ensure_rls / core.ensure_policy - RLS DDL that converges
-- ============================================================================
-- ALTER TABLE ... ENABLE ROW LEVEL SECURITY and DROP/CREATE POLICY take ACCESS
-- EXCLUSIVE, which queues every reader of the table behind the deploy. Running
-- them on every redeploy blocked the application for the whole deploy even
-- when nothing had changed. These run the DDL only when the table or policy
-- differs from what is requested: a policy carries the md5 of its requested
-- definition as its comment, so an unchanged policy is left alone.

CREATE OR REPLACE FUNCTION core.ensure_rls(p_table regclass, p_force boolean DEFAULT false)
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT (SELECT relrowsecurity FROM pg_class WHERE oid = p_table) THEN
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', p_table);
    END IF;
    IF p_force AND NOT (SELECT relforcerowsecurity FROM pg_class WHERE oid = p_table) THEN
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', p_table);
    END IF;
END;
$$;

-- Views get the same treatment, but their comments are for people, so the
-- fingerprint lives in a table. It records the requested statement and the
-- definition PostgreSQL stored, so a view edited by hand is replaced too.
CREATE TABLE IF NOT EXISTS internal.view_fingerprint (
    view_name   text PRIMARY KEY,
    statement_md5 text NOT NULL,
    stored_definition text NOT NULL
);

-- p_statement is the complete CREATE OR REPLACE VIEW statement for p_view.
-- Returns true when it ran DDL.
CREATE OR REPLACE FUNCTION core.ensure_view(p_view text, p_statement text)
RETURNS boolean
LANGUAGE plpgsql AS $$
BEGIN
    IF to_regclass(p_view) IS NOT NULL AND EXISTS (
        SELECT 1 FROM internal.view_fingerprint f
        WHERE f.view_name = p_view
          AND f.statement_md5 = md5(p_statement)
          AND f.stored_definition = pg_get_viewdef(to_regclass(p_view))
    ) THEN
        RETURN false;
    END IF;

    EXECUTE p_statement;
    INSERT INTO internal.view_fingerprint (view_name, statement_md5, stored_definition)
    VALUES (p_view, md5(p_statement), pg_get_viewdef(to_regclass(p_view)))
    ON CONFLICT (view_name) DO UPDATE
        SET statement_md5 = EXCLUDED.statement_md5,
            stored_definition = EXCLUDED.stored_definition;
    RETURN true;
END;
$$;

-- p_definition is everything after `CREATE POLICY name ON table`, e.g.
-- 'FOR SELECT TO app_customer USING (true)'. Returns true when it ran DDL.
CREATE OR REPLACE FUNCTION core.ensure_policy(p_table regclass, p_name text, p_definition text)
RETURNS boolean
LANGUAGE plpgsql AS $$
DECLARE
    v_fingerprint text := 'pgmi:' || md5(p_definition);
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_policy
        WHERE polrelid = p_table AND polname = p_name
          AND obj_description(oid, 'pg_policy') = v_fingerprint
    ) THEN
        RETURN false;
    END IF;

    EXECUTE format('DROP POLICY IF EXISTS %I ON %s', p_name, p_table);
    EXECUTE format('CREATE POLICY %I ON %s %s', p_name, p_table, p_definition);
    EXECUTE format('COMMENT ON POLICY %I ON %s IS %L', p_name, p_table, v_fingerprint);
    RETURN true;
END;
$$;

-- ============================================================================
-- core.apply_org_rls(regclass) - canonical multi-tenant RLS for domain tables
-- ============================================================================
-- One call installs the standard org-scoped ENABLE + FORCE RLS policy set on a
-- domain table, keyed on api.current_member_org_ids() — the same tenant anchor
-- the membership tables use. FORCE (not just ENABLE) means the policies bind
-- even for the table owner, so a SECURITY DEFINER handler that forgets an
-- explicit organization_id predicate is still constrained to the caller's
-- orgs. That closes the most common multi-tenant footgun: a kernel mutation
-- running as owner, where ENABLE-only RLS would not apply.
--
-- Requirements: the table has an `organization_id uuid` column (and, when
-- p_has_created_by, a `created_by_user_id uuid` column). The org predicate
-- matches the membership-table policies exactly: IN (SELECT unnest(...))
-- rather than ANY(api.current_member_org_ids()) — PostgreSQL only builds a
-- one-time InitPlan for the subquery form, so the STABLE function runs once
-- per query instead of once per candidate row. The scalar created_by check
-- is wrapped as (SELECT ...) for the same reason.

CREATE OR REPLACE FUNCTION core.apply_org_rls(
    p_table          regclass,
    p_has_created_by boolean DEFAULT true
) RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    v_rel          text;
    v_scope        text := 'organization_id IN (SELECT unnest(api.current_member_org_ids()))';
    v_insert_check text;
BEGIN
    SELECT relname INTO v_rel FROM pg_class WHERE oid = p_table;

    ASSERT EXISTS (
        SELECT 1 FROM pg_attribute
        WHERE attrelid = p_table AND attname = 'organization_id'
          AND attnum > 0 AND NOT attisdropped
    ), format('apply_org_rls: %s must have an organization_id column', p_table);

    IF p_has_created_by THEN
        ASSERT EXISTS (
            SELECT 1 FROM pg_attribute
            WHERE attrelid = p_table AND attname = 'created_by_user_id'
              AND attnum > 0 AND NOT attisdropped
        ), format('apply_org_rls: %s declares p_has_created_by but lacks created_by_user_id', p_table);
    END IF;

    PERFORM core.ensure_rls(p_table, p_force => true);

    PERFORM core.ensure_policy(p_table, v_rel || '_select',
        format('FOR SELECT USING (%s)', v_scope));

    v_insert_check := v_scope;
    IF p_has_created_by THEN
        v_insert_check := v_insert_check || ' AND created_by_user_id = (SELECT api.current_user_id())';
    END IF;
    PERFORM core.ensure_policy(p_table, v_rel || '_insert',
        format('FOR INSERT WITH CHECK (%s)', v_insert_check));

    PERFORM core.ensure_policy(p_table, v_rel || '_update',
        format('FOR UPDATE USING (%s) WITH CHECK (%s)', v_scope, v_scope));

    PERFORM core.ensure_policy(p_table, v_rel || '_delete',
        format('FOR DELETE USING (%s)', v_scope));
END;
$$;

COMMENT ON FUNCTION core.apply_org_rls(regclass, boolean) IS
    'Installs the standard org-scoped ENABLE+FORCE RLS policy set (select/insert/update/delete) on a domain table, keyed on api.current_member_org_ids(). Pass p_has_created_by=false for tables without a created_by_user_id column. FORCE RLS constrains the owner too, so SECURITY DEFINER kernels stay tenant-scoped even without an explicit predicate.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ core.apply_org_rls() - one-call multi-tenant RLS for domain tables';
END $$;
