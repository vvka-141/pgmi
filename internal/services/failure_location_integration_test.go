package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

const testGateHead = `BEGIN;
SAVEPOINT tests;
CALL pgmi_test();
ROLLBACK TO SAVEPOINT tests;
COMMIT;
`

func deployLocated(t *testing.T, files map[string]string) error {
	t.Helper()
	connString := testhelpers.RequireDatabase(t)
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const db = "pgmi_failure_location"
	testhelpers.CleanupTestDB(t, connString, db)
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, db) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return testhelpers.NewTestDeployer(t).Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        db,
		SourcePath:          dir,
		Force:               true,
	})
}

func requireDetail(t *testing.T, err error) *pgmi.ErrorDetail {
	t.Helper()
	if err == nil {
		t.Fatal("deploy succeeded; want a failure")
	}
	if out := pgmi.FormatError(err); strings.Contains(out, "pgmi_run_test_source") {
		t.Errorf("output names pgmi's internal wrapper:\n%s", out)
	}
	return pgmi.NewErrorDetail(err)
}

func TestFailureLocation_TestFileParseErrorReportsItsLine(t *testing.T) {
	d := requireDetail(t, deployLocated(t, map[string]string{
		"deploy.sql": testGateHead,
		"__test__/test_missing.sql": "SELECT 1;\nSELECT 2;\n" +
			"SELECT * FROM nosuchtable;\n",
	}))
	if d.FailedFile != "./__test__/test_missing.sql" || d.Script != "./__test__/test_missing.sql" || d.Line != 3 {
		t.Errorf("want ./__test__/test_missing.sql line 3, got failedFile=%q script=%q line=%d (%s)", d.FailedFile, d.Script, d.Line, d.Message)
	}
	if d.SQLState != "42P01" {
		t.Errorf("SQLSTATE %q, want the original 42P01", d.SQLState)
	}
}

func TestFailureLocation_TestFileRuntimeErrorNamesTheFileWithoutALine(t *testing.T) {
	d := requireDetail(t, deployLocated(t, map[string]string{
		"deploy.sql":               testGateHead,
		"__test__/test_divide.sql": "SELECT 1;\nSELECT 1/0;\n",
	}))
	if d.FailedFile != "./__test__/test_divide.sql" {
		t.Errorf("failedFile %q, want ./__test__/test_divide.sql (%s)", d.FailedFile, d.Message)
	}
	if d.Line != 0 {
		t.Errorf("a runtime error carries no position, so no line may be reported; got line %d of %q", d.Line, d.Script)
	}
	if d.SQLState != "22012" {
		t.Errorf("SQLSTATE %q, want the original 22012", d.SQLState)
	}
}

func TestFailureLocation_TailStatementReportsItsDeploySQLLine(t *testing.T) {
	deploySQL := testGateHead + "SELECT 1;\nSELECT 1/0;\n"
	d := requireDetail(t, deployLocated(t, map[string]string{
		"deploy.sql":           deploySQL,
		"__test__/test_ok.sql": "SELECT 1;\n",
	}))
	if d.Script != "deploy.sql" || d.Line != 7 || d.ScriptExpanded {
		t.Errorf("want deploy.sql line 7 of the file on disk, got script=%q line=%d expanded=%v (%s)", d.Script, d.Line, d.ScriptExpanded, d.Message)
	}
}

func TestFailureLocation_TailDOBlockReportsTheFailingLine(t *testing.T) {
	deploySQL := testGateHead + "DO $$\nBEGIN\n    PERFORM 1;\n    PERFORM 1/0;\nEND $$;\n"
	d := requireDetail(t, deployLocated(t, map[string]string{
		"deploy.sql":           deploySQL,
		"__test__/test_ok.sql": "SELECT 1;\n",
	}))
	if d.Script != "deploy.sql" || d.Line != 9 {
		t.Errorf("want deploy.sql line 9 (PERFORM 1/0), got script=%q line=%d (%s; where %q)", d.Script, d.Line, d.Message, d.Where)
	}
}

// A test path is quoted into generated SQL; one containing the dollar tag of
// the generated block must not break out of it.
func TestFailureLocation_TestPathWithTheDollarTagRuns(t *testing.T) {
	if err := deployLocated(t, map[string]string{
		"deploy.sql":                    testGateHead,
		"__test__/test_$pgmi_test$.sql": "SELECT 1;\n",
	}); err != nil {
		t.Fatalf("a test file whose name holds $pgmi_test$ must run: %v", err)
	}
}

func TestFailureLocation_AnErrorInDeploySQLItselfIsPlaced(t *testing.T) {
	d := requireDetail(t, deployLocated(t, map[string]string{
		"deploy.sql": "BEGIN;\n\nDO $$\nDECLARE\n    v_owner text := current_setting('pgmi.owner_role');\nBEGIN\n    PERFORM 1;\nEND $$;\n\nCOMMIT;\n",
	}))
	if d.Script != "deploy.sql" || d.Line != 5 || d.Phase != "deploy" {
		t.Errorf("want deploy.sql line 5, phase deploy; got script=%q line=%d phase=%q (%s)", d.Script, d.Line, d.Phase, d.Where)
	}
}

func TestFailureLocation_AFailingTestIsMarkedAndLined(t *testing.T) {
	test := "DO $$\nBEGIN\n    IF 1 = 1 THEN\n        RAISE EXCEPTION 'TEST FAILED: one is one';\n    END IF;\nEND $$;\n"
	d := requireDetail(t, deployLocated(t, map[string]string{
		"deploy.sql":                   testGateHead,
		"semantic/__test__/test_x.sql": test,
	}))
	if d.Phase != "test" || d.Script != "./semantic/__test__/test_x.sql" || d.Line != 4 {
		t.Errorf("want phase test at line 4 of the test; got phase=%q script=%q line=%d (%s)", d.Phase, d.Script, d.Line, d.Where)
	}
	if strings.Contains(d.Where, "IF 1 = 1") {
		t.Errorf("where must name the test file instead of inlining it: %q", d.Where)
	}
}

func TestFailureLocation_UnitsCommittedCountsOnlyCommittedUnits(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	dir := t.TempDir()
	deploySQL := "CREATE TABLE t1(x int);\nCOMMIT;\nBEGIN;\nINSERT INTO t1 VALUES (1);\nSELECT pg_sleep(5);\nCOMMIT;\n"
	if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte(deploySQL), 0o644); err != nil {
		t.Fatal(err)
	}
	const db = "pgmi_units_committed"
	testhelpers.CleanupTestDB(t, connString, db)
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, db) })
	deployer := testhelpers.NewTestDeployer(t).(*services.DeploymentService)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString: connString, MaintenanceDatabase: "postgres", DatabaseName: db, SourcePath: dir, Force: true,
	})
	if err == nil {
		t.Fatal("deploy succeeded; want the timeout")
	}
	r := deployer.LastResult()
	if r.UnitsCommitted != 1 || r.FailedUnit != 4 || r.ExecutionMode != "psql" {
		t.Errorf("only the head committed; got unitsCommitted=%d failedUnit=%d of %d, mode %q",
			r.UnitsCommitted, r.FailedUnit, r.ExecutionUnits, r.ExecutionMode)
	}
}
