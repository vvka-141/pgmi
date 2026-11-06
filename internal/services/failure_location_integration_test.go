package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return deployLocatedWithin(t, ctx, files)
}

func deployLocatedWithin(t *testing.T, ctx context.Context, files map[string]string) error {
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
	if d.FailedFile != "deploy.sql" || d.Script != "deploy.sql" || d.Line != 5 || d.Phase != "deploy" {
		t.Errorf("want deploy.sql line 5, phase deploy; got failedFile=%q script=%q line=%d phase=%q (%s)",
			d.FailedFile, d.Script, d.Line, d.Phase, d.Where)
	}
}

func TestFailureLocation_ACommentHeaderAboveATestKeepsTheLine(t *testing.T) {
	test := "-- ==========\n-- Test: x\n-- ==========\n\nDO $$\nBEGIN\n    RAISE EXCEPTION 'TEST FAILED: x';\nEND $$;\n"
	err := deployLocated(t, map[string]string{
		"deploy.sql":                   testGateHead,
		"semantic/__test__/test_x.sql": test,
	})
	d := requireDetail(t, err)
	if d.Script != "./semantic/__test__/test_x.sql" || d.Line != 7 {
		t.Errorf("want line 7 of the test below its header; got script=%q line=%d (%s)", d.Script, d.Line, d.Where)
	}
	if out := pgmi.FormatError(err); !strings.Contains(out, "LOCATION: ./semantic/__test__/test_x.sql line 7") {
		t.Errorf("LOCATION must carry the line:\n%s", out)
	}
}

func TestFailureLocation_AParseErrorInAFunctionBodyHasItsFileLine(t *testing.T) {
	for lang, body := range map[string]string{
		"sql":     "\n    SELECT 1\n    FRM nowhere;\n",
		"plpgsql": "\nBEGIN\n    PERFORM 1;\n    PERFRM 2;\nEND;\n",
	} {
		t.Run(lang, func(t *testing.T) {
			file := "-- semantic/fn.sql\nCREATE FUNCTION f() RETURNS void LANGUAGE " + lang + " AS $f$" + body + "$f$;\n"
			d := requireDetail(t, deployLocated(t, map[string]string{
				"deploy.sql":      "DO $$ BEGIN EXECUTE (SELECT content FROM pg_temp.pgmi_source_view WHERE path = './semantic/fn.sql'); END $$;\n",
				"semantic/fn.sql": file,
			}))
			want := map[string]int{"sql": 4, "plpgsql": 5}[lang]
			if d.FailedFile != "./semantic/fn.sql" || d.Script != "./semantic/fn.sql" || d.Line != want {
				t.Errorf("want ./semantic/fn.sql line %d; got failedFile=%q script=%q line=%d %q (%s)",
					want, d.FailedFile, d.Script, d.Line, d.SourceLine, d.Message)
			}
		})
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
	deployer := testhelpers.NewTestDeployer(t)
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

var slowProject = map[string]string{
	"deploy.sql": `BEGIN;
DO $$
DECLARE
    v_file record;
BEGIN
    FOR v_file IN SELECT path, content FROM pg_temp.pgmi_plan_view ORDER BY execution_order LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;
`,
	"semantic/zz_slow.sql": "SELECT pg_sleep(10);\n",
}

// pgmi ends a deploy that outlives its context with a cancel request, so the
// error PostgreSQL returns names the file that was running.
func TestFailureLocation_StoppedDeployNamesTheRunningFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(context.Context) (context.Context, context.CancelFunc)
		want int
	}{
		{"timeout", func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 2*time.Second)
		}, pgmi.ExitTimeout},
		{"interrupt", func(ctx context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(ctx)
			time.AfterFunc(2*time.Second, cancel)
			return ctx, cancel
		}, pgmi.ExitInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.stop(context.Background())
			defer cancel()
			start := time.Now()
			err := deployLocatedWithin(t, ctx, slowProject)
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("deploy took %s to stop; pg_sleep(10) outlived the cancel", elapsed)
			}
			d := requireDetail(t, err)
			if d.ExitCode != tc.want || d.SQLState != "57014" {
				t.Errorf("exit %d SQLSTATE %q, want exit %d SQLSTATE 57014: %v", d.ExitCode, d.SQLState, tc.want, err)
			}
			if d.FailedFile != "./semantic/zz_slow.sql" || d.Phase != "deploy" {
				t.Errorf("want failedFile ./semantic/zz_slow.sql in phase deploy, got %q %q (%s; where %q)", d.FailedFile, d.Phase, d.Message, d.Where)
			}
			if out := pgmi.FormatError(err); !strings.Contains(out, "LOCATION: ./semantic/zz_slow.sql") {
				t.Errorf("text output does not name the file:\n%s", out)
			}
		})
	}
}

// COMMIT AND CHAIN commits and opens the next transaction at once, so the
// session is never idle after the head; the head still committed.
func TestFailureLocation_CommitAndChainCountsTheHead(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	dir := t.TempDir()
	deploySQL := "BEGIN;\nCREATE TABLE IF NOT EXISTS chain_probe(id int);\nINSERT INTO chain_probe VALUES (1);\nCOMMIT AND CHAIN;\nSELECT 1/0;\n"
	if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte(deploySQL), 0o644); err != nil {
		t.Fatal(err)
	}
	const db = "pgmi_commit_and_chain"
	testhelpers.CleanupTestDB(t, connString, db)
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, db) })
	deployer := testhelpers.NewTestDeployer(t)
	err := deployer.Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString: connString, MaintenanceDatabase: "postgres", DatabaseName: db, SourcePath: dir, Force: true,
	})
	if err == nil {
		t.Fatal("deploy succeeded; want the division by zero")
	}
	r := deployer.LastResult()
	if r.UnitsCommitted != 1 || r.ExecutionMode != "psql" {
		t.Errorf("the head committed; got unitsCommitted=%d mode %q", r.UnitsCommitted, r.ExecutionMode)
	}
	var rows int
	if err := testhelpers.GetTestPool(t, connString, db).QueryRow(context.Background(), "SELECT count(*) FROM chain_probe").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("the committed head must have left one row, got %d (%v)", rows, err)
	}
}

func TestFailureLocation_SingleUnitCommitAndChainSucceeds(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	for _, c := range []struct {
		name, sql string
		committed bool
	}{
		{"commit and chain", "BEGIN;\nCREATE TABLE chain_single(id int);\nCOMMIT AND CHAIN;\n", true},
		{"no terminator", "BEGIN;\nCREATE TABLE chain_single(id int);\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte(c.sql), 0o644); err != nil {
				t.Fatal(err)
			}
			const db = "pgmi_commit_and_chain_single"
			testhelpers.CleanupTestDB(t, connString, db)
			t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, db) })
			deployer := testhelpers.NewTestDeployer(t)
			err := deployer.Deploy(context.Background(), pgmi.DeploymentConfig{
				ConnectionString: connString, MaintenanceDatabase: "postgres", DatabaseName: db, SourcePath: dir, Force: true,
			})
			r := deployer.LastResult()
			var exists bool
			if qerr := testhelpers.GetTestPool(t, connString, db).QueryRow(context.Background(),
				"SELECT to_regclass('chain_single') IS NOT NULL").Scan(&exists); qerr != nil {
				t.Fatal(qerr)
			}
			if c.committed && (err != nil || r.UnitsCommitted != 1 || !exists) {
				t.Errorf("want success with one committed unit and the table: err %v, unitsCommitted %d, table %t", err, r.UnitsCommitted, exists)
			}
			if !c.committed && (err == nil || !strings.Contains(err.Error(), "ended inside an open transaction") || r.UnitsCommitted != 0 || exists) {
				t.Errorf("want the open-transaction failure and nothing committed: err %v, unitsCommitted %d, table %t", err, r.UnitsCommitted, exists)
			}
		})
	}
}
