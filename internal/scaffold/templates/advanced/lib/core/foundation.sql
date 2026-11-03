/*
<pgmi-meta
    id="85c16de9-a1cc-491b-88e6-4db887f684c8"
    idempotent="true">
  <description>
    Core foundation code: core.ensure_view (views that converge without
    locking), core.apply_org_rls (multi-tenant RLS for domain tables) and
    core.execute_with_force_rls_lifted (deploy-time cross-tenant statements).
    The core.entity_id domain lives in foundation-structure.sql.
  </description>
  <sortKeys>
    <key>003/001</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing core entity foundation'; END $$;

-- ============================================================================
-- core.ensure_view - view DDL that converges
-- ============================================================================
-- CREATE OR REPLACE VIEW takes ACCESS EXCLUSIVE on the view even when nothing
-- changed, which queues every reader behind the deploy. ensure_view replaces a
-- view only when the requested statement, the stored definition or the view's
-- options (security_invoker, security_barrier, check_option) differ from what
-- internal.view_fingerprint recorded, so a view edited by hand is replaced
-- too. pg_get_viewdef omits the options. Drifted options are repaired by
-- re-running the statement: ALTER VIEW ... SET takes the same lock.
--
-- p_statement is the complete CREATE OR REPLACE VIEW statement for p_view.
-- Returns true when it ran DDL.
CREATE OR REPLACE FUNCTION core.ensure_view(p_view text, p_statement text)
RETURNS boolean
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT FROM internal.view_fingerprint f
        JOIN pg_class c ON c.oid = to_regclass(p_view)
        WHERE f.view_name = p_view
          AND f.statement_md5 = md5(p_statement)
          AND f.stored_definition = pg_get_viewdef(c.oid)
          AND f.stored_options IS NOT DISTINCT FROM c.reloptions
    ) THEN
        RETURN false;
    END IF;

    EXECUTE p_statement;
    INSERT INTO internal.view_fingerprint (view_name, statement_md5, stored_definition, stored_options)
    VALUES (p_view, md5(p_statement), pg_get_viewdef(to_regclass(p_view)),
            (SELECT reloptions FROM pg_class WHERE oid = to_regclass(p_view)))
    ON CONFLICT (view_name) DO UPDATE
        SET statement_md5 = EXCLUDED.statement_md5,
            stored_definition = EXCLUDED.stored_definition,
            stored_options = EXCLUDED.stored_options;
    RETURN true;
END;
$$;

-- ============================================================================
-- core.apply_org_rls(regclass) - canonical multi-tenant RLS for domain tables
-- ============================================================================
-- One call installs the standard org-scoped ENABLE + FORCE RLS policy set on a
-- domain table, keyed on api.current_organization_id(): a request works in one
-- organization, the one its organization session fixes. FORCE (not just ENABLE) means the policies bind
-- even for the table owner, so a SECURITY DEFINER handler that forgets an
-- explicit organization_id predicate is still constrained to the caller's
-- organization. That closes the most common multi-tenant footgun: a kernel mutation
-- running as owner, where ENABLE-only RLS would not apply.
--
-- Requirements: the table has an `organization_id uuid` column (and, when
-- p_has_created_by, a `created_by_user_id uuid` column). Both readers are
-- wrapped as (SELECT ...): PostgreSQL then builds a one-time InitPlan, so the
-- STABLE function runs once per query instead of once per candidate row. A
-- request without an organization matches no row.
--
-- It creates the policies, so call it once, from the run-once file that
-- creates the table.

CREATE OR REPLACE FUNCTION core.apply_org_rls(
    p_table          regclass,
    p_has_created_by boolean DEFAULT true
) RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    v_rel          text;
    v_scope        text := 'organization_id = (SELECT api.current_organization_id())';
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

    EXECUTE format('CREATE POLICY %I ON %s FOR SELECT USING (%s)',
        v_rel || '_select', p_table, v_scope);

    v_insert_check := v_scope;
    IF p_has_created_by THEN
        v_insert_check := v_insert_check || ' AND created_by_user_id = (SELECT api.current_principal_id())';
    END IF;
    EXECUTE format('CREATE POLICY %I ON %s FOR INSERT WITH CHECK (%s)',
        v_rel || '_insert', p_table, v_insert_check);
    EXECUTE format('CREATE POLICY %I ON %s FOR UPDATE USING (%s) WITH CHECK (%s)',
        v_rel || '_update', p_table, v_scope, v_scope);
    EXECUTE format('CREATE POLICY %I ON %s FOR DELETE USING (%s)',
        v_rel || '_delete', p_table, v_scope);
END;
$$;

COMMENT ON FUNCTION core.apply_org_rls(regclass, boolean) IS
    'Installs the standard org-scoped ENABLE+FORCE RLS policy set (select/insert/update/delete) on a domain table, keyed on api.current_organization_id(). Pass p_has_created_by=false for tables without a created_by_user_id column. FORCE RLS constrains the owner too, so SECURITY DEFINER kernels stay tenant-scoped even without an explicit predicate.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ core.apply_org_rls() - one-call multi-tenant RLS for domain tables';
END $$;

-- ============================================================================
-- core.execute_with_force_rls_lifted(regclass[], text) - deploy-time cross-tenant SQL
-- ============================================================================
-- FORCE ROW LEVEL SECURITY binds the owner too, so a deploy-time backfill or
-- fix-up running as the owner sees only the rows its (empty) tenant context
-- allows. This lifts FORCE on the listed tables, runs p_sql as the owner and
-- puts FORCE back. ALTER TABLE is transactional: other sessions never see the
-- lifted state, and an error in p_sql rolls the lift back with everything
-- else. ALTER TABLE takes ACCESS EXCLUSIVE on each table until the deploy
-- commits, so list only the tables the statement needs.
--
-- SECURITY INVOKER, never granted: only the owner can ALTER the tables, and a
-- caller that can is already able to read every row.
CREATE OR REPLACE FUNCTION core.execute_with_force_rls_lifted(p_tables regclass[], p_sql text)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_forced regclass[];
    v_table  regclass;
BEGIN
    v_forced := ARRAY(SELECT c.oid::regclass FROM pg_class c
                      WHERE c.oid = ANY (p_tables) AND c.relforcerowsecurity ORDER BY c.oid);

    FOREACH v_table IN ARRAY v_forced LOOP
        EXECUTE format('ALTER TABLE %s NO FORCE ROW LEVEL SECURITY', v_table);
    END LOOP;

    EXECUTE p_sql;

    FOREACH v_table IN ARRAY v_forced LOOP
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', v_table);
    END LOOP;
END;
$$;

COMMENT ON FUNCTION core.execute_with_force_rls_lifted(regclass[], text) IS
    'Deploy-time only: runs p_sql as the owner with FORCE ROW LEVEL SECURITY lifted on the listed tables, then restores it. Not granted to any application role.';
