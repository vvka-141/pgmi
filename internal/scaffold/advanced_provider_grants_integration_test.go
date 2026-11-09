package scaffold_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

// Databricks Lakebase as observed on 2026-11-08 (PGMI-823): event triggers in
// public grant the provider's roles on every new schema and table, those
// trigger functions stay executable by PUBLIC, and every database holds a
// schema the provider owns. The template's exact-grant guards refuse that
// shape until provider_role_pattern and provider_schemas name it; row-level
// security still keeps the provider's reader out of the refusal and exchange logs.
func TestAdvancedTemplate_DeploysBesideProviderGrants(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	const db = "pgmi_advanced_provider"
	providerRoles := []string{"pgmi_lbx_reader", "pgmi_lbx_writer", "pgmi_lbx_control"}

	admin := testhelpers.GetTestPool(t, connString, "postgres")
	cleanup := func() {
		testhelpers.CleanupTestDB(t, connString, db)
		for _, role := range append([]string{db + "_admin", db + "_api", db + "_kernel", db + "_owner", db + "_jobs"}, providerRoles...) {
			_, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+role)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	for _, role := range providerRoles {
		if _, err := admin.Exec(ctx, "CREATE ROLE "+role+" NOLOGIN"); err != nil {
			t.Fatalf("create provider role %s: %v", role, err)
		}
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create the database: %v", err)
	}

	target := testhelpers.GetTestPool(t, connString, db)
	if _, err := target.Exec(ctx, `
CREATE SCHEMA __db_system AUTHORIZATION pgmi_lbx_control;

CREATE FUNCTION public.grant_usage_on_new_schema() RETURNS event_trigger LANGUAGE plpgsql AS $$
DECLARE c record;
BEGIN
    FOR c IN SELECT object_identity FROM pg_event_trigger_ddl_commands() WHERE command_tag = 'CREATE SCHEMA' LOOP
        EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %s TO pgmi_lbx_writer', c.object_identity);
        EXECUTE format('GRANT USAGE ON SCHEMA %s TO pgmi_lbx_reader', c.object_identity);
    END LOOP;
END $$;

CREATE FUNCTION public.grant_select_on_new_objects() RETURNS event_trigger LANGUAGE plpgsql AS $$
DECLARE c record;
BEGIN
    FOR c IN SELECT object_identity FROM pg_event_trigger_ddl_commands()
             WHERE command_tag IN ('CREATE TABLE', 'CREATE VIEW') AND object_type IN ('table', 'view') LOOP
        EXECUTE format('GRANT SELECT ON %s TO pgmi_lbx_reader', c.object_identity);
        EXECUTE format('GRANT ALL ON %s TO pgmi_lbx_control', c.object_identity);
    END LOOP;
END $$;

CREATE EVENT TRIGGER on_create_schema ON ddl_command_end WHEN TAG IN ('CREATE SCHEMA')
    EXECUTE FUNCTION public.grant_usage_on_new_schema();
CREATE EVENT TRIGGER on_create_table_or_view ON ddl_command_end WHEN TAG IN ('CREATE TABLE', 'CREATE VIEW')
    EXECUTE FUNCTION public.grant_select_on_new_objects();
`); err != nil {
		t.Fatalf("install the provider's event triggers: %v", err)
	}

	projectPath := scaffoldAdvanced(t, nil)
	params := map[string]string{"env": "test", "database_admin_password": "TestPassword123!"}

	err := deployAdvanced(t, connString, db, projectPath, false, params)
	if err == nil || !strings.Contains(err.Error(), "TEST FAILED") {
		t.Fatalf("without the provider settings a guard must refuse the provider's grants, got: %v", err)
	}

	params["provider_role_pattern"] = `pgmi\_lbx\_%`
	params["provider_schemas"] = `["__db_system"]`
	for _, run := range []string{"first", "second"} {
		if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
			t.Fatalf("%s deploy with the provider settings: %v", run, err)
		}
	}

	for _, schema := range []string{"internal", "core", "api", "common", "membership", "extensions"} {
		var create, usage bool
		if err := target.QueryRow(ctx, "SELECT has_schema_privilege('pgmi_lbx_writer', $1, 'CREATE'), has_schema_privilege('pgmi_lbx_writer', $1, 'USAGE')", schema).Scan(&create, &usage); err != nil {
			t.Fatalf("read the writer's privileges on %s: %v", schema, err)
		}
		if create || !usage {
			t.Fatalf("the provider's writer must keep USAGE and lose CREATE on %s (a schema on a search_path), got CREATE=%v USAGE=%v", schema, create, usage)
		}
	}

	var whole, rows bool
	if err := target.QueryRow(ctx, `
SELECT has_table_privilege('pgmi_lbx_control', 'membership.organization', 'TRUNCATE')
    OR has_table_privilege('pgmi_lbx_control', 'membership.organization', 'TRIGGER')
    OR has_table_privilege('pgmi_lbx_control', 'membership.organization', 'REFERENCES')
    OR has_table_privilege('pgmi_lbx_control', 'membership.organization', 'MAINTAIN'),
       has_table_privilege('pgmi_lbx_control', 'membership.organization', 'SELECT, INSERT, UPDATE, DELETE')`).Scan(&whole, &rows); err != nil {
		t.Fatalf("read the admin group's privileges on membership.organization: %v", err)
	}
	if whole || !rows {
		t.Fatalf("the provider's admin group must lose TRUNCATE, TRIGGER, REFERENCES and MAINTAIN and keep its row privileges, got whole-table=%v rows=%v", whole, rows)
	}

	logs := []string{"api.access_denial", "api.exchange", "api.rest_exchange", "api.rpc_exchange", "api.mcp_exchange"}
	for _, table := range logs {
		var granted bool
		if err := target.QueryRow(ctx, "SELECT has_table_privilege('pgmi_lbx_reader', $1, 'SELECT')", table).Scan(&granted); err != nil || !granted {
			t.Fatalf("the provider's trigger must have granted its reader %s (else this test proves nothing): %v, %v", table, granted, err)
		}
	}

	tx, err := target.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO api.access_denial (bucket, handler_object_id, reason, organization_object_id, actor_kind)
SELECT date_trunc('minute', now()), object_id, 'permission_missing', gen_random_uuid(), 'human' FROM api.handler LIMIT 1`); err != nil {
		t.Fatalf("plant a refusal row: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO api.rest_exchange (handler_object_id, context, request)
SELECT object_id, jsonb_build_object('organizationId', gen_random_uuid()),
       ROW('POST', '/x', ''::extensions.hstore, convert_to('secret body', 'UTF8'), '{}'::jsonb)::api.rest_request
FROM api.handler LIMIT 1`); err != nil {
		t.Fatalf("plant an exchange row: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE pgmi_lbx_reader"); err != nil {
		t.Fatalf("become the provider's reader: %v", err)
	}
	for _, table := range logs {
		var seen int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&seen); err != nil {
			t.Fatalf("read %s as the provider's reader: %v", table, err)
		}
		if seen != 0 {
			t.Fatalf("the provider's reader read %d row(s) of %s; row-level security must hide every organization's rows from it", seen, table)
		}
	}
}
