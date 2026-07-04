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

    EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', p_table);
    EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', p_table);

    EXECUTE format('DROP POLICY IF EXISTS %I ON %s', v_rel || '_select', p_table);
    EXECUTE format('CREATE POLICY %I ON %s FOR SELECT USING (%s)',
        v_rel || '_select', p_table, v_scope);

    v_insert_check := v_scope;
    IF p_has_created_by THEN
        v_insert_check := v_insert_check || ' AND created_by_user_id = (SELECT api.current_user_id())';
    END IF;
    EXECUTE format('DROP POLICY IF EXISTS %I ON %s', v_rel || '_insert', p_table);
    EXECUTE format('CREATE POLICY %I ON %s FOR INSERT WITH CHECK (%s)',
        v_rel || '_insert', p_table, v_insert_check);

    EXECUTE format('DROP POLICY IF EXISTS %I ON %s', v_rel || '_update', p_table);
    EXECUTE format('CREATE POLICY %I ON %s FOR UPDATE USING (%s) WITH CHECK (%s)',
        v_rel || '_update', p_table, v_scope, v_scope);

    EXECUTE format('DROP POLICY IF EXISTS %I ON %s', v_rel || '_delete', p_table);
    EXECUTE format('CREATE POLICY %I ON %s FOR DELETE USING (%s)',
        v_rel || '_delete', p_table, v_scope);
END;
$$;

COMMENT ON FUNCTION core.apply_org_rls(regclass, boolean) IS
    'Installs the standard org-scoped ENABLE+FORCE RLS policy set (select/insert/update/delete) on a domain table, keyed on api.current_member_org_ids(). Pass p_has_created_by=false for tables without a created_by_user_id column. FORCE RLS constrains the owner too, so SECURITY DEFINER kernels stay tenant-scoped even without an explicit predicate.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ core.apply_org_rls() - one-call multi-tenant RLS for domain tables';
END $$;
