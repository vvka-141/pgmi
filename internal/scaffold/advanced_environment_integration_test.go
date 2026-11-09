package scaffold_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

const devProbe = `/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000020" idempotent="false">
  <sortKeys><key>900/000</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.dev_probe (id int);
SELECT pg_temp.exempt_from_rls('core.dev_probe', 'test probe');
`

func scaffoldAdvanced(t *testing.T, files map[string]string) string {
	t.Helper()
	projectPath := filepath.Join(t.TempDir(), "env")
	if err := scaffold.NewScaffolder(false).CreateProject("env", "advanced", projectPath); err != nil {
		t.Fatalf("scaffold the advanced template: %v", err)
	}
	writeProbes(t, projectPath, files, nil)
	return projectPath
}

func deployAdvanced(t *testing.T, connString, db, projectPath string, overwrite bool, params map[string]string) error {
	t.Helper()
	return testhelpers.NewTestDeployer(t).Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        db,
		SourcePath:          projectPath,
		Overwrite:           overwrite,
		Force:               true,
		Parameters:          params,
	})
}

// Every parameter refusal fires before anything is created, on a fresh database.
func TestAdvancedTemplate_RefusesBadParameters(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := scaffoldAdvanced(t, nil)
	const pw = "TestPassword123!"

	for _, tc := range []struct {
		name    string
		files   map[string]string
		params  map[string]string
		want    string
		notWant string
	}{
		{name: "no env", params: map[string]string{"database_admin_password": pw},
			want: "Missing required parameter: env", notWant: "database_admin_password"},
		{name: "no env and no password", params: map[string]string{},
			want: "Missing required parameter: env", notWant: "database_admin_password"},
		{name: "blank env", params: map[string]string{"env": "  "},
			want: "Missing required parameter: env"},
		{name: "unknown env", params: map[string]string{"env": "staging", "database_admin_password": pw},
			want: "env must be dev, test or prod"},
		{name: "prod without password", params: map[string]string{"env": "prod"},
			want: "Missing required parameters: database_admin_password. Set PGMI_PARAM_DATABASE_ADMIN_PASSWORD", notWant: "--param <key>"},
		{name: "prod with blank password", params: map[string]string{"env": "prod", "database_admin_password": " "},
			want: "Missing required parameters: database_admin_password"},
		{name: "relabel naming another label", params: map[string]string{"env": "prod", "relabel_environment": "test", "database_admin_password": pw},
			want: "relabel_environment=test does not name the label env=prod"},
		{name: "relabel on a new database", params: map[string]string{"env": "prod", "relabel_environment": "prod", "database_admin_password": pw},
			want: "carries no environment label yet"},
		{name: "upgrade of a new database", params: map[string]string{"env": "prod", "upgrade": "true", "database_admin_password": pw},
			want: "a first deploy is not an upgrade"},
		{name: "upgrade not a boolean", params: map[string]string{"env": "prod", "upgrade": "yes", "database_admin_password": pw},
			want: "upgrade must be true or false"},
		{name: "retention not a whole number", params: map[string]string{"env": "test", "database_admin_password": pw, "exchange_retention_days": "7.5"},
			want: "exchange_retention_days must be a whole number of days"},
		{name: "retention zero", params: map[string]string{"env": "test", "database_admin_password": pw, "exchange_retention_days": "0"},
			want: "exchange_retention_days must be a whole number of days"},
		{name: "challenge with a line break", params: map[string]string{"env": "test", "database_admin_password": pw, "www_authenticate": "Bearer\r\nSet-Cookie: x=1"},
			want: "www_authenticate must be one header value"},
		{name: "undeclared parameter", params: map[string]string{"env": "test", "database_admin_password": pw, "no_such_param": "x"},
			want: "Unknown parameters: no_such_param"},
		{name: "mfa flag not a boolean", params: map[string]string{"env": "test", "database_admin_password": pw, "platform_mfa_required": "yes"},
			want: "platform_mfa_required must be true or false"},
		{name: "api base path with a trailing slash", params: map[string]string{"env": "test", "database_admin_password": pw, "api_base_path": "/api/"},
			want: "api_base_path must be / or a path like /api"},
		{name: "provider pattern matching a template role", params: map[string]string{"env": "dev", "provider_role_pattern": "pgmi\\_advanced\\_%"},
			want: "provider_role_pattern matches the template's own role(s): database_admin_role (pgmi_advanced_params_admin)"},
		{name: "provider schemas not an array", params: map[string]string{"env": "dev", "provider_schemas": "__db_system"},
			want: "provider_schemas must be a JSON array of schema names"},
		{name: "provider schemas holding a number", params: map[string]string{"env": "dev", "provider_schemas": `["__db_system", 1]`},
			want: "provider_schemas must be a JSON array of schema names"},
		{name: "a file reads an undeclared setting", files: map[string]string{"core/setting_probe.sql": "/*\n<pgmi-meta id=\"f6470000-0000-4000-8000-000000000001\" idempotent=\"true\">\n  <sortKeys><key>005/100/probe/1</key></sortKeys>\n</pgmi-meta>\n*/\nSELECT pg_temp.deployment_setting('no_such_setting');\n"},
			params: map[string]string{"env": "dev"}, want: `Required deployment parameter "no_such_setting" not set`},
		{name: "role name past the identifier limit", params: map[string]string{"env": "dev", "database_owner_role": strings.Repeat("r", 64)},
			want: "Role name(s) longer than 63 bytes: database_owner_role (64 bytes)"},
		{name: "upgrade and relabel together", params: map[string]string{"env": "prod", "upgrade": "true", "relabel_environment": "prod", "database_admin_password": pw},
			want: "upgrade=true and relabel_environment=prod together"},
		{name: "test under a dev folder", files: map[string]string{"core/dev/__test__/test_probe.sql": "SELECT 1;\n"},
			params: map[string]string{"env": "dev"}, want: "Test file(s) under a dev/ folder: ./core/dev/__test__/test_probe.sql"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := projectPath
			if tc.files != nil {
				path = scaffoldAdvanced(t, tc.files)
			}
			db := "pgmi_advanced_params"
			defer testhelpers.CleanupTestDB(t, connString, db)
			err := deployAdvanced(t, connString, db, path, true, tc.params)
			if err == nil {
				t.Fatal("deploy succeeded; it must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got: %v", tc.want, err)
			}
			if tc.notWant != "" && strings.Contains(err.Error(), tc.notWant) {
				t.Fatalf("the error must not mention %q, got: %v", tc.notWant, err)
			}
			// A parameter that is missing or malformed raises 22023, so CI can
			// tell "secret not wired" from a failed migration; refusals that
			// depend on the database's state keep their own SQLSTATE.
			stateful := map[string]bool{"relabel on a new database": true, "upgrade of a new database": true, "test under a dev folder": true}
			var pgErr *pgconn.PgError
			if !stateful[tc.name] && (!errors.As(err, &pgErr) || pgErr.Code != "22023") {
				t.Errorf("want SQLSTATE 22023 invalid_parameter_value, got %v", err)
			}
			var schemas int
			if qerr := testhelpers.GetTestPool(t, connString, db).QueryRow(context.Background(),
				"SELECT count(*) FROM pg_namespace WHERE nspname IN ('internal', 'core', 'api', 'membership')").Scan(&schemas); qerr != nil {
				t.Fatalf("count schemas: %v", qerr)
			}
			if schemas != 0 {
				t.Errorf("the refused deploy created %d schemas", schemas)
			}
		})
	}
}

// The database keeps the environment it was deployed as. A redeploy naming
// another is refused before any role is touched, so env=dev can never reset a
// prod database's passwords to the dev default. A relabel takes an explicit
// parameter, and files under dev/ deploy only while the database is dev.
func TestAdvancedTemplate_EnvironmentLabel(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := scaffoldAdvanced(t, map[string]string{"core/dev/dev_probe.sql": devProbe})
	const db = "pgmi_advanced_env_label"
	const pw = "ProdPassword123!"
	defer testhelpers.CleanupTestDB(t, connString, db)

	state := func(t *testing.T) (label string, devProbe bool, password string) {
		t.Helper()
		pool := testhelpers.GetTestPool(t, connString, db)
		ctx := context.Background()
		if err := pool.QueryRow(ctx, `SELECT (SELECT label FROM internal.environment), to_regclass('core.dev_probe') IS NOT NULL`).Scan(&label, &devProbe); err != nil {
			t.Fatalf("read the label: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname = $1`, db+"_admin").Scan(&password); err != nil {
			t.Fatalf("read the admin password: %v", err)
		}
		return
	}
	expect := func(t *testing.T, wantLabel string, wantDevProbe bool) string {
		t.Helper()
		label, probe, password := state(t)
		if label != wantLabel {
			t.Errorf("label = %q, want %q", label, wantLabel)
		}
		if probe != wantDevProbe {
			t.Errorf("core.dev_probe present = %t, want %t", probe, wantDevProbe)
		}
		return password
	}
	refused := func(t *testing.T, params map[string]string, want string) {
		t.Helper()
		err := deployAdvanced(t, connString, db, projectPath, false, params)
		if err == nil {
			t.Fatalf("deploy with %v succeeded; it must be refused", params)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got: %v", want, err)
		}
	}

	if err := deployAdvanced(t, connString, db, projectPath, true, map[string]string{"env": "prod", "database_admin_password": pw}); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	prodPassword := expect(t, "prod", false)

	refused(t, map[string]string{"env": "dev"}, "env=dev does not match database pgmi_advanced_env_label's environment label (prod)")
	refused(t, map[string]string{"env": "dev", "relabel_environment": "dev"}, "Missing required parameters: database_admin_password")
	refused(t, map[string]string{"env": "prod", "relabel_environment": "prod", "database_admin_password": pw}, "is already labelled prod")
	refused(t, map[string]string{"env": "test", "upgrade": "true", "database_admin_password": pw}, "env=test does not match")
	if got := expect(t, "prod", false); got != prodPassword {
		t.Fatal("a refused deploy changed the admin role's password")
	}

	if err := deployAdvanced(t, connString, db, projectPath, false, map[string]string{"env": "prod", "upgrade": "true", "database_admin_password": pw}); err != nil {
		t.Fatalf("upgrade of a prod database: %v", err)
	}

	if err := deployAdvanced(t, connString, db, projectPath, false, map[string]string{"env": "test", "relabel_environment": "test", "database_admin_password": pw}); err != nil {
		t.Fatalf("relabel to test: %v", err)
	}
	expect(t, "test", false)

	if err := deployAdvanced(t, connString, db, projectPath, false, map[string]string{"env": "dev", "relabel_environment": "dev", "database_admin_password": pw}); err != nil {
		t.Fatalf("relabel to dev: %v", err)
	}
	expect(t, "dev", true)
	devEntry := func(t *testing.T) (function bool, openDevSessions, strayIssuers int) {
		t.Helper()
		pool := testhelpers.GetTestPool(t, connString, db)
		err := pool.QueryRow(context.Background(), `
			SELECT to_regprocedure('api.open_dev_login_session(text, text, text, text)') IS NOT NULL,
			       (SELECT count(*) FROM membership.login_session s
			        JOIN membership.external_identity e ON e.object_id = s.external_identity_object_id
			        JOIN membership.identity_issuer i ON i.id = e.issuer_id
			        WHERE i.kind = 'development' AND s.revoked_at IS NULL),
			       (SELECT count(*) FROM membership.identity_issuer i
			        WHERE i.kind = 'development'
			          AND NOT EXISTS (SELECT FROM membership.external_identity e WHERE e.issuer_id = i.id))`).
			Scan(&function, &openDevSessions, &strayIssuers)
		if err != nil {
			t.Fatalf("read the dev entry points: %v", err)
		}
		return
	}
	{
		pool := testhelpers.GetTestPool(t, connString, db)
		var token *string
		if err := pool.QueryRow(context.Background(), `SELECT api.open_dev_login_session('dev', 'eve-001', 'eve@example.test', 'Eve')`).Scan(&token); err != nil || token == nil {
			t.Fatalf("a dev database must open a dev login session: token %v, err %v", token, err)
		}
		if _, err := pool.Exec(context.Background(), `INSERT INTO membership.identity_issuer (id, kind) VALUES ('stray', 'development')`); err != nil {
			t.Fatalf("add an unreferenced development issuer: %v", err)
		}
		pool.Close()
	}
	if function, open, stray := devEntry(t); !function || open != 1 || stray != 1 {
		t.Fatalf("on dev: function %t, open dev sessions %d, unreferenced dev issuers %d; want true, 1, 1", function, open, stray)
	}

	// core/dev/dev_probe.sql ran while the database was dev; on test it is not
	// deployed, and that is not run-once drift.
	if err := deployAdvanced(t, connString, db, projectPath, false, map[string]string{"env": "test", "relabel_environment": "test", "database_admin_password": pw}); err != nil {
		t.Fatalf("relabel back to test: %v", err)
	}
	expect(t, "test", true)
	if function, open, stray := devEntry(t); function || open != 0 || stray != 0 {
		t.Fatalf("after the relabel from dev: function %t, open dev sessions %d, unreferenced dev issuers %d; want false, 0, 0", function, open, stray)
	}
}

// A database name that pushes a default role name past the identifier limit
// is refused before any role exists: PostgreSQL would truncate the name, and
// the next deploy would not find the role it created.
func TestAdvancedTemplate_RefusesARoleNameTooLong(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	db := "pgmi_long_" + strings.Repeat("n", 50)
	defer testhelpers.CleanupTestDB(t, connString, db)

	err := deployAdvanced(t, connString, db, scaffoldAdvanced(t, nil), true, map[string]string{"env": "dev"})
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "22023" ||
		!strings.Contains(err.Error(), "database_kernel_role (67 bytes)") {
		t.Fatalf("want 22023 naming database_kernel_role, got %v", err)
	}
	var roles int
	pool := testhelpers.GetTestPool(t, connString, "postgres")
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'pgmi_long_%'").Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if roles != 0 {
		t.Fatalf("%d role(s) created before the refusal", roles)
	}
}

// Roles belong to the cluster. An admin role that exists but is not a member
// of this database's owner role belongs to something else: the deploy refuses
// it and leaves its password and memberships alone.
func TestAdvancedTemplate_RefusesAForeignAdminRole(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	const db, role = "pgmi_foreign_admin", "pgmi_foreign_admin_login"
	defer testhelpers.CleanupTestDB(t, connString, db)
	pool := testhelpers.GetTestPool(t, connString, "postgres")
	if _, err := pool.Exec(ctx, "CREATE ROLE "+role+" LOGIN PASSWORD 'prod-secret-123'"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), "DROP ROLE IF EXISTS "+role) }()
	var before string
	if err := pool.QueryRow(ctx, "SELECT rolpassword FROM pg_authid WHERE rolname = $1", role).Scan(&before); err != nil {
		t.Fatal(err)
	}

	err := deployAdvanced(t, connString, db, scaffoldAdvanced(t, nil), true,
		map[string]string{"env": "dev", "database_admin_role": role})
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "22023" || !strings.Contains(err.Error(), "database_admin_role") {
		t.Fatalf("want 22023 naming database_admin_role, got %v", err)
	}

	var after string
	var memberships int
	if err := pool.QueryRow(ctx, `SELECT a.rolpassword, (SELECT count(*) FROM pg_auth_members m WHERE m.member = a.oid)
		FROM pg_authid a WHERE a.rolname = $1`, role).Scan(&after, &memberships); err != nil {
		t.Fatal(err)
	}
	if after != before || memberships != 0 {
		t.Fatalf("foreign role touched: password changed %t, memberships %d", after != before, memberships)
	}
}

func TestAdvancedTemplate_RefusesAForeignTemplateRole(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	const db, mallory = "pgmi_foreign_roles", "pgmi_foreign_roles_mallory"
	defer testhelpers.CleanupTestDB(t, connString, db)
	pool := testhelpers.GetTestPool(t, connString, "postgres")
	drop := func() {
		for _, r := range []string{db + "_api", db + "_owner", db + "_kernel", db + "_jobs", mallory} {
			_, _ = pool.Exec(context.Background(), "DROP ROLE IF EXISTS "+r)
		}
	}
	projectPath := scaffoldAdvanced(t, nil)

	for _, tc := range []struct{ name, setup, role string }{
		{"api role created by a CREATEROLE peer", "SET ROLE " + mallory + "; CREATE ROLE " + db + "_api NOLOGIN; RESET ROLE", db + "_api"},
		{"owner role with a foreign member", "CREATE ROLE " + db + "_owner NOLOGIN; GRANT " + db + "_owner TO " + mallory, db + "_owner"},
		{"kernel role with a foreign member", "CREATE ROLE " + db + "_kernel NOLOGIN; GRANT " + db + "_kernel TO " + mallory, db + "_kernel"},
		{"jobs role created by a CREATEROLE peer", "SET ROLE " + mallory + "; CREATE ROLE " + db + "_jobs LOGIN; RESET ROLE", db + "_jobs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drop()
			defer drop()
			if _, err := pool.Exec(ctx, "CREATE ROLE "+mallory+" LOGIN CREATEROLE; "+tc.setup); err != nil {
				t.Fatal(err)
			}
			err := deployAdvanced(t, connString, db, projectPath, true, map[string]string{"env": "dev"})
			var pgErr *pgconn.PgError
			if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "22023" || !strings.Contains(err.Error(), tc.role) {
				t.Fatalf("want 22023 naming %s, got %v", tc.role, err)
			}
			var memberships int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_auth_members WHERE roleid = to_regrole($1)`, tc.role).Scan(&memberships); err != nil {
				t.Fatal(err)
			}
			if memberships != 1 {
				t.Fatalf("%s memberships changed: %d", tc.role, memberships)
			}
		})
	}
}

// A jobs role nobody registered as a service principal may be anyone's: a
// CREATEROLE login can create it and drop itself, leaving no trace a
// superuser's role would not leave. The deploy grants it nothing and takes
// back what it held; registration is what earns the grants.
func TestAdvancedTemplate_IgnoresAnUnregisteredJobsLogin(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	const db, mallory = "pgmi_unregistered_jobs", "pgmi_unregistered_jobs_mallory"
	const jobs = db + "_jobs"
	server := testhelpers.GetTestPool(t, connString, "postgres")
	drop := func() {
		for _, r := range []string{jobs, mallory} {
			_, _ = server.Exec(context.Background(), "DROP ROLE IF EXISTS "+r)
		}
	}
	defer drop()
	defer testhelpers.CleanupTestDB(t, connString, db)
	projectPath := scaffoldAdvanced(t, nil)
	params := map[string]string{"env": "dev"}

	privileges := func(t *testing.T) (routines int, usage, connect bool) {
		t.Helper()
		pool := testhelpers.GetTestPool(t, connString, db)
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM pg_proc
			        WHERE pronamespace = 'api'::regnamespace AND proname LIKE 'job\_%'
			          AND has_function_privilege($1, oid, 'EXECUTE')),
			       has_schema_privilege($1, 'api', 'USAGE'),
			       has_database_privilege($1, current_database(), 'CONNECT')`, jobs).Scan(&routines, &usage, &connect); err != nil {
			t.Fatal(err)
		}
		return routines, usage, connect
	}
	wantNone := func(t *testing.T) {
		t.Helper()
		if routines, usage, connect := privileges(t); routines != 0 || usage || connect {
			t.Fatalf("unregistered %s holds EXECUTE on %d job(s), USAGE on api %t, CONNECT %t", jobs, routines, usage, connect)
		}
	}

	for _, tc := range []struct{ name, setup string }{
		{"created by a CREATEROLE login that then dropped itself",
			"CREATE ROLE " + mallory + " LOGIN CREATEROLE; SET ROLE " + mallory + "; CREATE ROLE " + jobs + " LOGIN; RESET ROLE; DROP ROLE " + mallory},
		{"created by a superuser", "CREATE ROLE " + jobs + " LOGIN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drop()
			defer drop()
			if _, err := server.Exec(ctx, tc.setup); err != nil {
				t.Fatal(err)
			}
			if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
				t.Fatalf("deploy: %v", err)
			}
			wantNone(t)
		})
	}

	t.Run("loses its grants once unregistered", func(t *testing.T) {
		drop()
		defer drop()
		if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		pool := testhelpers.GetTestPool(t, connString, db)
		if _, err := pool.Exec(ctx, "CREATE ROLE "+jobs+" LOGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `SELECT membership.fn_register_service_principal('service.jobs', 'Background jobs', $1)`, jobs); err != nil {
			t.Fatal(err)
		}
		if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
			t.Fatalf("redeploy registered: %v", err)
		}
		if routines, usage, connect := privileges(t); routines == 0 || !usage || !connect {
			t.Fatalf("registered %s holds EXECUTE on %d job(s), USAGE on api %t, CONNECT %t", jobs, routines, usage, connect)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM membership.service_principal WHERE database_role = $1`, jobs); err != nil {
			t.Fatal(err)
		}
		if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
			t.Fatalf("redeploy unregistered: %v", err)
		}
		wantNone(t)
	})

	t.Run("an admin role with a member the deploy did not grant", func(t *testing.T) {
		drop()
		defer drop()
		if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		if _, err := server.Exec(ctx, "CREATE ROLE "+mallory+" LOGIN; GRANT "+db+"_admin TO "+mallory); err != nil {
			t.Fatal(err)
		}
		err := deployAdvanced(t, connString, db, projectPath, false, params)
		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "22023" || !strings.Contains(err.Error(), db+"_admin") {
			t.Fatalf("want 22023 naming %s_admin, got %v", db, err)
		}
	})
}

func TestAdvancedTemplate_RedeploysWithAGatewayLoginInTheApiRole(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	const db, gateway = "pgmi_gateway_member", "pgmi_gateway_member_login"
	defer testhelpers.CleanupTestDB(t, connString, db)
	pool := testhelpers.GetTestPool(t, connString, "postgres")
	defer func() { _, _ = pool.Exec(context.Background(), "DROP ROLE IF EXISTS "+gateway) }()
	projectPath := scaffoldAdvanced(t, nil)
	params := map[string]string{"env": "dev"}

	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE ROLE "+gateway+" LOGIN; GRANT "+db+"_api TO "+gateway); err != nil {
		t.Fatal(err)
	}
	if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
		t.Fatalf("redeploy: %v", err)
	}
	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("overwrite redeploy: %v", err)
	}
}

func TestAdvancedTemplate_DeploysWithQuotedOrCustomRoleNames(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := scaffoldAdvanced(t, nil)
	for _, tc := range []struct {
		db     string
		params map[string]string
	}{
		{"pgmi-hyphen-app", map[string]string{"env": "dev"}},
		{"pgmi_custom_owner", map[string]string{"env": "dev", "database_owner_role": "pgmi_custom_owner_ownr"}},
	} {
		t.Run(tc.db, func(t *testing.T) {
			defer testhelpers.CleanupTestDB(t, connString, tc.db)
			if err := deployAdvanced(t, connString, tc.db, projectPath, true, tc.params); err != nil {
				t.Fatalf("deploy: %v", err)
			}
		})
	}
}
