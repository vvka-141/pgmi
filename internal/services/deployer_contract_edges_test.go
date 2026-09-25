package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func deployProject(t *testing.T, dbName string, files map[string]string) error {
	t.Helper()
	connString := testhelpers.RequireDatabase(t)
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, dbName) })
	return testhelpers.NewTestDeployer(t).Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        dbName,
		SourcePath:          writeProject(t, files),
		Overwrite:           true,
		Force:               true,
	})
}

// __tests__ is accepted in four places that must agree: the Go dunder regex,
// the loader's test-path pattern, and two CHECK constraints in schema.sql.
// Every fixture uses __test__, so tightening any one of them to the singular
// passed the whole suite. Here a __tests__ test must run, and must not also
// appear in pgmi_source_view, where a plan loop would execute it as a
// migration.
func TestDeploy_DoubleSTestsDirectory(t *testing.T) {
	err := deployProject(t, "pgmi_double_s_tests", map[string]string{
		"deploy.sql": `
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_temp.pgmi_source_view WHERE path LIKE '%/__tests__/%') THEN
        RAISE EXCEPTION 'a __tests__ file reached pgmi_source_view';
    END IF;
END $$;
BEGIN;
CALL pgmi_test();
COMMIT;`,
		"__tests__/test_marker.sql": `DO $$ BEGIN RAISE EXCEPTION 'ran-from-double-s'; END $$;`,
	})
	if err == nil || !strings.Contains(err.Error(), "ran-from-double-s") {
		t.Fatalf("the __tests__ test must run and fail the deploy with its own message, got: %v", err)
	}
}

// Only the root deploy.sql is withheld from the session. A nested one is an
// ordinary project file, as DEPLOY-GUIDE promises.
func TestDeploy_NestedDeploySQLIsLoaded(t *testing.T) {
	err := deployProject(t, "pgmi_nested_deploy_sql", map[string]string{
		"deploy.sql": `
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_temp.pgmi_source_view WHERE path = './examples/deploy.sql') THEN
        RAISE EXCEPTION 'nested examples/deploy.sql is missing from pgmi_source_view';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_temp.pgmi_source_view WHERE path = './deploy.sql') THEN
        RAISE EXCEPTION 'the root deploy.sql leaked into pgmi_source_view';
    END IF;
END $$;`,
		"examples/deploy.sql": `SELECT 1;`,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A <pgmi-meta> block without sortKeys takes the NULLIF branch in
// pgmi_plan_view. No file in the repo exercised it, so reducing it to a bare
// COALESCE would drop such a file from the plan unnoticed.
func TestDeploy_MetadataWithoutSortKeysStaysInPlan(t *testing.T) {
	err := deployProject(t, "pgmi_meta_no_sortkeys", map[string]string{
		"deploy.sql": `
DO $$ BEGIN
    IF (SELECT count(*) FROM pg_temp.pgmi_plan_view WHERE path = './migrations/001.sql') IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'a file with metadata but no sortKeys must appear once in pgmi_plan_view';
    END IF;
    IF (SELECT id FROM pg_temp.pgmi_plan_view WHERE path = './migrations/001.sql')
       IS DISTINCT FROM '6f1c2d4e-0261-4000-8000-000000000001'::uuid THEN
        RAISE EXCEPTION 'the plan row must carry the declared id';
    END IF;
END $$;`,
		"migrations/001.sql": `/*
<pgmi-meta id="6f1c2d4e-0261-4000-8000-000000000001" idempotent="true">
  <description>no sortKeys</description>
</pgmi-meta>
*/
SELECT 1;`,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A malformed pattern must fail with pgmi's message rather than a bare 42P22,
// and a pattern that matches nothing must fail rather than gate the deploy on
// no tests at all.
func TestDeploy_TestPatternFailsFast(t *testing.T) {
	for _, tc := range []struct {
		db, pattern, want string
	}{
		{"pgmi_pattern_invalid", "[unclosed", "Invalid regex pattern"},
		{"pgmi_pattern_nomatch", "nomatch", "no tests matched"},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			err := deployProject(t, tc.db, map[string]string{
				"deploy.sql":          "BEGIN;\nCALL pgmi_test('" + tc.pattern + "');\nCOMMIT;",
				"__test__/test_a.sql": `SELECT 1;`,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("pattern %q: want an error containing %q, got: %v", tc.pattern, tc.want, err)
			}
		})
	}
}

// A mistyped -d creates a new database and exits 0. The machine-readable
// result must say so, since a pipeline or an AI agent never sees the log line.
func TestDeploy_ReportsWhetherItCreatedTheDatabase(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const db = "pgmi_created_flag"
	testhelpers.CleanupTestDB(t, connString, db)
	defer testhelpers.CleanupTestDB(t, connString, db)

	project := writeProject(t, map[string]string{"deploy.sql": "SELECT 1;"})
	deploy := func() bool {
		t.Helper()
		d := testhelpers.NewTestDeployer(t).(*services.DeploymentService)
		if err := d.Deploy(context.Background(), pgmi.DeploymentConfig{
			ConnectionString: connString, MaintenanceDatabase: "postgres",
			DatabaseName: db, SourcePath: project, Force: true,
		}); err != nil {
			t.Fatal(err)
		}
		return d.LastResult().Created
	}
	if !deploy() {
		t.Error("first deploy created the database but reported created=false")
	}
	if deploy() {
		t.Error("second deploy reused the database but reported created=true")
	}
}
