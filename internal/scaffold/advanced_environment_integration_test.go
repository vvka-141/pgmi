package scaffold_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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
			want: "Missing required parameters: database_admin_password"},
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

	// core/dev/dev_probe.sql ran while the database was dev; on test it is not
	// deployed, and that is not run-once drift.
	if err := deployAdvanced(t, connString, db, projectPath, false, map[string]string{"env": "test", "relabel_environment": "test", "database_admin_password": pw}); err != nil {
		t.Fatalf("relabel back to test: %v", err)
	}
	expect(t, "test", true)
}
