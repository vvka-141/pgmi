package services_test

import (
	"context"
	"testing"

	"github.com/vvka-141/pgmi/internal/contract"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/params"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestExecuteDeploySQL_DirectExecution(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_direct_exec"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	pool := testhelpers.GetTestPool(t, connString, testDB)
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	l := loader.NewLoader()
	files := []pgmi.FileMetadata{
		{
			Path:        "./migrations/001.sql",
			Content:     "CREATE TABLE deploy_test(id int);",
			Checksum:    "a000000000000000000000000000000000000000000000000000000000000001",
			ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000001",
		},
	}
	if err := l.LoadFilesIntoSession(ctx, conn, files); err != nil {
		t.Fatalf("Failed to load files: %v", err)
	}
	if err := l.LoadParametersIntoSession(ctx, conn, map[string]string{}); err != nil {
		t.Fatalf("Failed to load params: %v", err)
	}
	if _, err := contract.Apply(ctx, conn, ""); err != nil {
		t.Fatalf("Failed to apply contract: %v", err)
	}

	deploySQL := `
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    )
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
`
	_, err = conn.Exec(ctx, deploySQL)
	if err != nil {
		t.Fatalf("deploy.sql execution failed: %v", err)
	}

	var tableExists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'deploy_test'
		)
	`).Scan(&tableExists); err != nil {
		t.Fatalf("Failed to query table: %v", err)
	}
	if !tableExists {
		t.Error("Expected 'deploy_test' table to exist after deployment")
	}
}

func TestExecuteDeploySQL_SyntaxError(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_exec_syntax"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	pool := testhelpers.GetTestPool(t, connString, testDB)
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	_, err = conn.Exec(ctx, "SELCT INVALID SYNTAX HERE;")
	if err == nil {
		t.Fatal("Expected error from invalid SQL")
	}
}

func TestExecuteDeploySQL_MultipleStatements(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_exec_multi"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	pool := testhelpers.GetTestPool(t, connString, testDB)
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	l := loader.NewLoader()
	if err := l.LoadFilesIntoSession(ctx, conn, nil); err != nil {
		t.Fatalf("Failed to load files: %v", err)
	}
	if err := l.LoadParametersIntoSession(ctx, conn, map[string]string{}); err != nil {
		t.Fatalf("Failed to load params: %v", err)
	}
	if _, err := contract.Apply(ctx, conn, ""); err != nil {
		t.Fatalf("Failed to apply contract: %v", err)
	}

	deploySQL := `
		CREATE TABLE multi_t1(id int);
		CREATE TABLE multi_t2(id int);
		CREATE TABLE multi_t3(id int);
	`
	_, err = conn.Exec(ctx, deploySQL)
	if err != nil {
		t.Fatalf("deploy.sql execution failed: %v", err)
	}

	for _, tableName := range []string{"multi_t1", "multi_t2", "multi_t3"} {
		var exists bool
		err = conn.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables WHERE table_name = $1
			)
		`, tableName).Scan(&exists)
		if err != nil {
			t.Fatalf("Failed to check table %s: %v", tableName, err)
		}
		if !exists {
			t.Errorf("Expected table %s to exist", tableName)
		}
	}
}

func TestExecuteDeploySQL_ContextCancellation(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	testDB := "pgmi_test_exec_cancel"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	pool := testhelpers.GetTestPool(t, connString, testDB)
	defer pool.Close()

	ctx, cancel := context.WithCancel(context.Background())

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	l := loader.NewLoader()
	if err := l.LoadFilesIntoSession(ctx, conn, nil); err != nil {
		t.Fatalf("Failed to load files: %v", err)
	}
	if err := l.LoadParametersIntoSession(ctx, conn, map[string]string{}); err != nil {
		t.Fatalf("Failed to load params: %v", err)
	}
	if _, err := contract.Apply(ctx, conn, ""); err != nil {
		t.Fatalf("Failed to apply contract: %v", err)
	}

	cancel()

	_, err = conn.Exec(ctx, "SELECT 1")
	if err == nil {
		t.Fatal("Expected error after context cancellation")
	}
}
