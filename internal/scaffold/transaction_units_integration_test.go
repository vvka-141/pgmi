package scaffold_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// These tests pin the head/tail execution contract (PGMI-210): deploy.sql runs
// as one simple-query message up to the first top-level transaction terminator
// (atomic mode), then one message per top-level statement (psql mode). The
// headline capability is CREATE INDEX CONCURRENTLY after a top-level COMMIT —
// impossible when the whole script ships as one message, because PostgreSQL
// wraps a multi-statement simple query in an implicit transaction block.

func deployProject(t *testing.T, deploySQL string, files map[string]string, testDB string) error {
	t.Helper()
	projectPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("write deploy.sql: %v", err)
	}
	for name, content := range files {
		full := filepath.Join(projectPath, name)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	deployer := testhelpers.NewTestDeployer(t)
	return deployer.Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString:    testhelpers.RequireDatabase(t),
		MaintenanceDatabase: "postgres",
		DatabaseName:        testDB,
		SourcePath:          projectPath,
		Overwrite:           true,
		Force:               true,
	})
}

// The realistic zero-downtime sequence: schema change, first concurrent index,
// an explicitly-transactional backfill, second concurrent index. Under
// last-terminator anchoring the backfill's COMMIT would pull the first CIC
// back inside the implicit block (25001); first-terminator anchoring makes
// every phase work.
func TestDeployInterleavedConcurrentIndexes(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	testDB := "pgmi_test_cic_interleaved"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	deploySQL := `
BEGIN;
CREATE TABLE cic_probe (id int, doubled int);
INSERT INTO cic_probe SELECT g, NULL FROM generate_series(1, 100) g;
COMMIT;
CREATE INDEX CONCURRENTLY idx_cic_first ON cic_probe (id);
BEGIN;
UPDATE cic_probe SET doubled = id * 2;
COMMIT;
CREATE INDEX CONCURRENTLY idx_cic_second ON cic_probe (doubled);
`
	if err := deployProject(t, deploySQL, nil, testDB); err != nil {
		t.Fatalf("interleaved CIC deploy failed: %v", err)
	}

	pool := testhelpers.GetTestPool(t, connString, testDB)

	for _, index := range []string{"idx_cic_first", "idx_cic_second"} {
		var valid bool
		err := pool.QueryRow(ctx,
			"SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1",
			index).Scan(&valid)
		if err != nil {
			t.Fatalf("index %s not found: %v", index, err)
		}
		if !valid {
			t.Errorf("index %s exists but is INVALID", index)
		}
	}

	var backfilled int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM cic_probe WHERE doubled = id * 2").Scan(&backfilled); err != nil {
		t.Fatalf("count backfill: %v", err)
	}
	if backfilled != 100 {
		t.Errorf("backfill applied to %d rows, want 100", backfilled)
	}
}

// A tail BEGIN ... COMMIT is a real transaction: a failure inside it rolls
// back that transaction only, while earlier autocommitted tail units stay
// applied — the documented mid-tail failure contract.
func TestDeployTailTransactionIsAtomic(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	testDB := "pgmi_test_tail_atomic"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	deploySQL := `
BEGIN;
CREATE TABLE head_t (id int);
COMMIT;
CREATE TABLE tail_before (id int);
BEGIN;
INSERT INTO tail_before VALUES (1);
SELECT 1/0;
COMMIT;
`
	if err := deployProject(t, deploySQL, nil, testDB); err == nil {
		t.Fatal("expected the division-by-zero inside the tail transaction to fail the deploy")
	}

	pool := testhelpers.GetTestPool(t, connString, testDB)

	var headExists, tailExists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.head_t') IS NOT NULL, to_regclass('public.tail_before') IS NOT NULL").Scan(&headExists, &tailExists); err != nil {
		t.Fatalf("existence probe: %v", err)
	}
	if !headExists {
		t.Error("head transaction was committed before the tail failure; head_t must exist")
	}
	if !tailExists {
		t.Error("autocommitted tail unit before the failing transaction must stay applied; tail_before must exist")
	}

	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tail_before").Scan(&rows); err != nil {
		t.Fatalf("count tail_before: %v", err)
	}
	if rows != 0 {
		t.Errorf("the failed tail transaction must roll back its INSERT, found %d rows", rows)
	}
}

// pgmi's session temp views survive the head COMMIT (session-scoped) and stay
// readable from tail units.
func TestDeployTailReadsSessionViews(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	testDB := "pgmi_test_tail_views"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Deliberately no BEGIN: a head that ends its implicit transaction block
	// with a bare COMMIT works too — PostgreSQL prints a harmless
	// "there is no transaction in progress" warning and commits the work.
	deploySQL := `
SELECT 1;
COMMIT;
CREATE TABLE tail_probe AS SELECT count(*)::int AS n FROM pg_temp.pgmi_source_view;
`
	files := map[string]string{
		"migrations/001.sql": "SELECT 1;\n",
	}
	if err := deployProject(t, deploySQL, files, testDB); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	pool := testhelpers.GetTestPool(t, connString, testDB)
	var n int
	if err := pool.QueryRow(ctx, "SELECT n FROM tail_probe").Scan(&n); err != nil {
		t.Fatalf("read tail_probe: %v", err)
	}
	if n != 1 {
		t.Errorf("pgmi_source_view visible from tail reported %d files, want 1", n)
	}
}
