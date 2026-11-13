package scaffold_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func basicProbe(id, idempotent, body string) string {
	return fmt.Sprintf("/*\n<pgmi-meta id=\"%s\" idempotent=\"%s\">\n</pgmi-meta>\n*/\n%s", id, idempotent, body)
}

const (
	basicProbeID   = "ffffffff-0464-4000-8000-000000000001"
	basicProbeBody = "CREATE TABLE run_once_probe (id int);\nINSERT INTO run_once_probe VALUES (1);\n"
)

func deployBasic(t *testing.T, connString, db, projectPath string, overwrite bool) error {
	t.Helper()
	return deployBasicWith(t, connString, db, projectPath, overwrite, map[string]string{"env": "dev"})
}

func deployBasicWith(t *testing.T, connString, db, projectPath string, overwrite bool, params map[string]string) error {
	t.Helper()
	return testhelpers.NewTestDeployer(t).Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        db,
		SourcePath:          projectPath,
		Parameters:          params,
		Overwrite:           overwrite,
		Force:               overwrite,
	})
}

// A run-once file runs on the first deploy only; a file without a run-once
// header runs on every deploy. Only a non-repeatable probe tells them apart.
func TestBasicTemplate_RunOnceAndReRun(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := filepath.Join(t.TempDir(), "proj")
	if err := scaffold.NewScaffolder(false).CreateProject("proj", "basic", projectPath); err != nil {
		t.Fatal(err)
	}
	writeProbes(t, projectPath, map[string]string{
		"physical/900_probe.sql": basicProbe(basicProbeID, "false", basicProbeBody),
		"semantic/zz_probe.sql":  "INSERT INTO run_once_probe VALUES (2);\n",
	}, nil)

	const db = "pgmi_basic_run_once"
	defer testhelpers.CleanupTestDB(t, connString, db)
	if err := deployBasic(t, connString, db, projectPath, true); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if err := deployBasic(t, connString, db, projectPath, false); err != nil {
		t.Fatalf("second deploy: %v", err)
	}

	var runOnce, reRun int
	if err := testhelpers.GetTestPool(t, connString, db).QueryRow(context.Background(),
		"SELECT count(*) FILTER (WHERE id = 1), count(*) FILTER (WHERE id = 2) FROM run_once_probe").Scan(&runOnce, &reRun); err != nil {
		t.Fatal(err)
	}
	if runOnce != 1 || reRun != 2 {
		t.Errorf("after two deploys: run-once file ran %d time(s), re-run file %d, want 1 and 2", runOnce, reRun)
	}
}

// A run-once file is skipped once it ran, so an edit, a move, a flip to re-run
// or a removal would silently reach only fresh databases. The basic deploy.sql
// refuses such a deploy before any file runs and names the file.
func TestBasicTemplate_RefusesRunOnceDrift(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	original := map[string]string{"physical/900_probe.sql": basicProbe(basicProbeID, "false", basicProbeBody)}

	tests := []struct {
		name    string
		second  map[string]string
		wantMsg string
	}{
		{"content edited", map[string]string{"physical/900_probe.sql": basicProbe(basicProbeID, "false",
			"CREATE TABLE run_once_probe (id int, note text);\nINSERT INTO run_once_probe VALUES (1);\n")},
			"./physical/900_probe.sql: content edited"},
		{"moved", map[string]string{"physical/moved/900_probe.sql": basicProbe(basicProbeID, "false", basicProbeBody)},
			"./physical/moved/900_probe.sql: moved from ./physical/900_probe.sql"},
		{"flipped to re-run", map[string]string{"physical/900_probe.sql": basicProbe(basicProbeID, "true", basicProbeBody)},
			"./physical/900_probe.sql: now idempotent=\"true\""},
		{"removed", map[string]string{},
			"./physical/900_probe.sql: removed from the project"},
		{"comment, whitespace and keyword case only", map[string]string{"physical/900_probe.sql": basicProbe(basicProbeID, "false",
			"-- the probe\ncreate   table run_once_probe\n    (id int); /* unchanged */\ninsert into run_once_probe values (1);\n")},
			""},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectPath := filepath.Join(t.TempDir(), "proj")
			if err := scaffold.NewScaffolder(false).CreateProject("proj", "basic", projectPath); err != nil {
				t.Fatal(err)
			}
			writeProbes(t, projectPath, original, nil)

			db := fmt.Sprintf("pgmi_basic_drift_%d", i)
			defer testhelpers.CleanupTestDB(t, connString, db)
			if err := deployBasic(t, connString, db, projectPath, true); err != nil {
				t.Fatalf("first deploy: %v", err)
			}

			second := map[string]string{"physical/000_marker.sql": basicProbe("ffffffff-0464-4000-8000-000000000002", "false",
				"CREATE TABLE drift_marker (id int);\n")}
			for k, v := range tt.second {
				second[k] = v
			}
			writeProbes(t, projectPath, second, original)

			err := deployBasic(t, connString, db, projectPath, false)
			if tt.wantMsg == "" {
				if err != nil {
					t.Fatalf("a comment, whitespace and keyword case edit refused the deploy: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("deploy succeeded after a run-once file drifted")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("want the error to contain %q, got: %v", tt.wantMsg, err)
			}
			if hint := pgmi.NewErrorDetail(err).Hint; !strings.Contains(hint, "-d "+db+" ") || strings.Contains(hint, "--force") {
				t.Errorf("the drift hint must name -d %s and not --force: %q", db, hint)
			}
			var markerApplied bool
			if qerr := testhelpers.GetTestPool(t, connString, db).QueryRow(context.Background(),
				"SELECT to_regclass('public.drift_marker') IS NOT NULL").Scan(&markerApplied); qerr != nil {
				t.Fatal(qerr)
			}
			if markerApplied {
				t.Error("the refused deploy applied a file: drift_marker exists")
			}
		})
	}
}

// env is required and closed: only dev seeds the Demo project, so a CI deploy
// that names prod, or names nothing, never puts demo data into a real database.
func TestBasicTemplate_EnvDecidesTheSeed(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := filepath.Join(t.TempDir(), "proj")
	if err := scaffold.NewScaffolder(false).CreateProject("proj", "basic", projectPath); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		env      string
		wantErr  string
		wantDemo bool
	}{
		{env: "dev", wantDemo: true},
		{env: "test"},
		{env: "prod"},
		{env: "", wantErr: "Missing required parameter: env"},
		{env: "production", wantErr: "Parameter env must be dev, test or prod, got 'production'"},
	}
	for i, tt := range tests {
		t.Run("env="+tt.env, func(t *testing.T) {
			db := fmt.Sprintf("pgmi_basic_env_%d", i)
			defer testhelpers.CleanupTestDB(t, connString, db)
			params := map[string]string{}
			if tt.env != "" {
				params["env"] = tt.env
			}
			err := deployBasicWith(t, connString, db, projectPath, true, params)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want an error containing %q, got %v", tt.wantErr, err)
				}
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "22023" {
					t.Errorf("want SQLSTATE 22023 invalid_parameter_value, got %v", err)
				}
				if tt.env == "" && (pgErr == nil || !strings.Contains(pgErr.Hint, "--param env=dev") || !strings.Contains(pgErr.Hint, "PGMI_PARAM_ENV")) {
					t.Errorf("the missing-env hint must name the local and the CI fix, got %+v", pgErr)
				}
				var tables int
				if qerr := testhelpers.GetTestPool(t, connString, db).QueryRow(context.Background(),
					"SELECT count(*) FROM pg_tables WHERE schemaname IN ('public', 'deployment')").Scan(&tables); qerr != nil {
					t.Fatal(qerr)
				}
				if tables != 0 {
					t.Errorf("the refused deploy left %d table(s) behind", tables)
				}
				return
			}
			if err != nil {
				t.Fatalf("deploy: %v", err)
			}
			var demo bool
			if qerr := testhelpers.GetTestPool(t, connString, db).QueryRow(context.Background(),
				"SELECT EXISTS (SELECT FROM project WHERE name = 'Demo')").Scan(&demo); qerr != nil {
				t.Fatal(qerr)
			}
			if demo != tt.wantDemo {
				t.Errorf("Demo project exists = %v, want %v", demo, tt.wantDemo)
			}
		})
	}
}

// A physical/ file without the run-once header used to re-run: the first
// deploy passed and the second failed on "already exists", far from the cause.
// The first deploy now refuses it and names the file and the header.
func TestBasicTemplate_RefusesPhysicalFileWithoutRunOnceHeader(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	for i, file := range []string{
		"ALTER TABLE project ADD COLUMN archived boolean;\n",
		basicProbe("ffffffff-0472-4000-8000-000000000001", "true", "ALTER TABLE project ADD COLUMN archived boolean;\n"),
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			projectPath := filepath.Join(t.TempDir(), "proj")
			if err := scaffold.NewScaffolder(false).CreateProject("proj", "basic", projectPath); err != nil {
				t.Fatal(err)
			}
			writeProbes(t, projectPath, map[string]string{"physical/003_project_archived.sql": file}, nil)

			db := fmt.Sprintf("pgmi_basic_header_%d", i)
			defer testhelpers.CleanupTestDB(t, connString, db)
			err := deployBasic(t, connString, db, projectPath, true)
			if err == nil {
				t.Fatal("the first deploy accepted a physical/ file that would re-run")
			}
			for _, want := range []string{"physical/ file(s) without a run-once header", "./physical/003_project_archived.sql"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("want the error to contain %q, got: %v", want, err)
				}
			}
			if hint := pgmi.NewErrorDetail(err).Hint; !strings.Contains(hint, `idempotent="false"`) {
				t.Errorf("want the hint to show the header, got %q", hint)
			}
		})
	}
}

// A run-once file with two sort keys ran at both keys on the first deploy and
// at neither afterwards. The basic deploy.sql refuses it before anything runs.
func TestBasicTemplate_RefusesRunOnceFileWithSeveralSortKeys(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := filepath.Join(t.TempDir(), "proj")
	if err := scaffold.NewScaffolder(false).CreateProject("proj", "basic", projectPath); err != nil {
		t.Fatal(err)
	}
	writeProbes(t, projectPath, map[string]string{"physical/003_keys.sql": `/*
<pgmi-meta id="ffffffff-0489-4000-8000-000000000002" idempotent="false">
  <sortKeys><key>./physical/003a</key><key>./physical/003b</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE keys_probe (id int);
`}, nil)

	const db = "pgmi_basic_keys"
	defer testhelpers.CleanupTestDB(t, connString, db)
	err := deployBasic(t, connString, db, projectPath, true)
	if err == nil || !strings.Contains(err.Error(), "./physical/003_keys.sql") ||
		!strings.Contains(err.Error(), "Run-once file(s) with more than one sort key") {
		t.Fatalf("want the two-key run-once file refused by name, got: %v", err)
	}
	var created bool
	if qerr := testhelpers.GetTestPool(t, connString, db).QueryRow(context.Background(),
		"SELECT to_regclass('public.keys_probe') IS NOT NULL").Scan(&created); qerr != nil {
		t.Fatal(qerr)
	}
	if created {
		t.Error("the refused deploy created keys_probe")
	}
}

func TestBasicTemplate_RefusesSQLFileOutsideItsFolders(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	for i, path := range []string{"seed.sql", "tests/test_x.sql"} {
		t.Run(path, func(t *testing.T) {
			projectPath := filepath.Join(t.TempDir(), "proj")
			if err := scaffold.NewScaffolder(false).CreateProject("proj", "basic", projectPath); err != nil {
				t.Fatal(err)
			}
			writeProbes(t, projectPath, map[string]string{path: "CREATE TABLE stray ();\n"}, nil)

			db := fmt.Sprintf("pgmi_basic_stray_%d", i)
			defer testhelpers.CleanupTestDB(t, connString, db)
			err := deployBasic(t, connString, db, projectPath, true)
			if err == nil {
				t.Fatalf("the deploy accepted %s, which never runs", path)
			}
			for _, want := range []string{"SQL file(s) outside physical/ and semantic/", "./" + path} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("want the error to contain %q, got: %v", want, err)
				}
			}
			hint := pgmi.NewErrorDetail(err).Hint
			for _, folder := range []string{"physical/", "semantic/", "__test__/"} {
				if !strings.Contains(hint, folder) {
					t.Errorf("want the hint to name %s, got %q", folder, hint)
				}
			}
		})
	}
}

// Every deploy re-runs semantic/, and CREATE OR REPLACE VIEW takes ACCESS
// EXCLUSIVE on the view. Behind a reader's open transaction that request
// would wait without bound while every new reader queued behind it; the
// template's lock_timeout fails the deploy with 55P03 instead.
func TestBasicTemplate_LockWaitIsBounded(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := filepath.Join(t.TempDir(), "proj")
	if err := scaffold.NewScaffolder(false).CreateProject("proj", "basic", projectPath); err != nil {
		t.Fatal(err)
	}

	const db = "pgmi_basic_lock_wait"
	defer testhelpers.CleanupTestDB(t, connString, db)
	if err := deployBasic(t, connString, db, projectPath, true); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	ctx := context.Background()
	reader, err := testhelpers.GetTestPool(t, connString, db).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback(ctx) }()
	if _, err := reader.Exec(ctx, "SELECT FROM project_view"); err != nil {
		t.Fatal(err)
	}

	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = testhelpers.NewTestDeployer(t).Deploy(bounded, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        db,
		SourcePath:          projectPath,
		Parameters:          map[string]string{"env": "dev"},
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("redeploy behind an open reader of project_view: want 55P03 from lock_timeout, got %v", err)
	}
}
