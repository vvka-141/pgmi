package services_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/params"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestExecuteDeploySQL_PopulatesPlan(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_exec_plan"
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
	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create unittest schema: %v", err)
	}

	deploySQL := `
DO $$
BEGIN
    PERFORM pg_temp.pgmi_plan_command('SELECT 1;');
    PERFORM pg_temp.pgmi_plan_file('./migrations/001.sql');
END $$;
`
	_, err = conn.Exec(ctx, deploySQL)
	if err != nil {
		t.Fatalf("deploy.sql execution failed: %v", err)
	}

	var planCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_plan").Scan(&planCount); err != nil {
		t.Fatalf("Failed to query plan: %v", err)
	}
	if planCount != 2 {
		t.Errorf("Expected 2 plan entries, got %d", planCount)
	}
}

func TestExecuteDeploySQL_EmptyPlan(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_exec_empty"
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
	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create unittest schema: %v", err)
	}

	_, err = conn.Exec(ctx, "SELECT 1;")
	if err != nil {
		t.Fatalf("Empty deploy.sql failed: %v", err)
	}

	var planCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_plan").Scan(&planCount); err != nil {
		t.Fatalf("Failed to query plan: %v", err)
	}
	if planCount != 0 {
		t.Errorf("Expected 0 plan entries, got %d", planCount)
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

func TestExecutePlannedCommands_SingleCommand(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_exec_single"
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
	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create unittest schema: %v", err)
	}

	_, err = conn.Exec(ctx, "SELECT pg_temp.pgmi_plan_command('CREATE TABLE planned_test(id int);')")
	if err != nil {
		t.Fatalf("Failed to plan command: %v", err)
	}

	rows, err := conn.Query(ctx, "SELECT ordinal, command_sql FROM pg_temp.pgmi_plan ORDER BY ordinal")
	if err != nil {
		t.Fatalf("Failed to query plan: %v", err)
	}

	type command struct {
		ordinal int
		sql     string
	}
	var commands []command
	for rows.Next() {
		var cmd command
		if err := rows.Scan(&cmd.ordinal, &cmd.sql); err != nil {
			t.Fatalf("Failed to scan: %v", err)
		}
		commands = append(commands, cmd)
	}
	rows.Close()

	if len(commands) != 1 {
		t.Fatalf("Expected 1 command, got %d", len(commands))
	}

	_, err = conn.Exec(ctx, commands[0].sql)
	if err != nil {
		t.Fatalf("Failed to execute planned command: %v", err)
	}

	var exists bool
	err = conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'planned_test'
		)
	`).Scan(&exists)
	if err != nil {
		t.Fatalf("Failed to check table: %v", err)
	}
	if !exists {
		t.Error("Expected 'planned_test' table to exist after execution")
	}
}

func TestExecutePlannedCommands_MultipleCommands(t *testing.T) {
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
	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create unittest schema: %v", err)
	}

	_, err = conn.Exec(ctx, `
		SELECT pg_temp.pgmi_plan_command('CREATE TABLE multi_t1(id int);');
		SELECT pg_temp.pgmi_plan_command('CREATE TABLE multi_t2(id int);');
		SELECT pg_temp.pgmi_plan_command('CREATE TABLE multi_t3(id int);');
	`)
	if err != nil {
		t.Fatalf("Failed to plan commands: %v", err)
	}

	rows, err := conn.Query(ctx, "SELECT ordinal, command_sql FROM pg_temp.pgmi_plan ORDER BY ordinal")
	if err != nil {
		t.Fatalf("Failed to query plan: %v", err)
	}

	type command struct {
		ordinal int
		sql     string
	}
	var commands []command
	for rows.Next() {
		var cmd command
		if err := rows.Scan(&cmd.ordinal, &cmd.sql); err != nil {
			t.Fatalf("Failed to scan: %v", err)
		}
		commands = append(commands, cmd)
	}
	rows.Close()

	if len(commands) != 3 {
		t.Fatalf("Expected 3 commands, got %d", len(commands))
	}

	for _, cmd := range commands {
		_, err = conn.Exec(ctx, cmd.sql)
		if err != nil {
			t.Fatalf("Failed to execute command %d: %v", cmd.ordinal, err)
		}
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

func TestExecutePlannedCommands_EmptyPlan(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_exec_noop"
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
	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create unittest schema: %v", err)
	}

	rows, err := conn.Query(ctx, "SELECT ordinal, command_sql FROM pg_temp.pgmi_plan ORDER BY ordinal")
	if err != nil {
		t.Fatalf("Failed to query empty plan: %v", err)
	}

	count := 0
	for rows.Next() {
		count++
	}
	rows.Close()

	if count != 0 {
		t.Errorf("Expected 0 commands in empty plan, got %d", count)
	}
}

func TestExecutePlannedCommands_FailingCommand(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_exec_fail"
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
	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create unittest schema: %v", err)
	}

	_, err = conn.Exec(ctx, "SELECT pg_temp.pgmi_plan_command('SELECT * FROM nonexistent_table_xyz;')")
	if err != nil {
		t.Fatalf("Failed to plan command: %v", err)
	}

	rows, err := conn.Query(ctx, "SELECT ordinal, command_sql FROM pg_temp.pgmi_plan ORDER BY ordinal")
	if err != nil {
		t.Fatalf("Failed to query plan: %v", err)
	}

	var ordinal int
	var sql string
	if rows.Next() {
		if err := rows.Scan(&ordinal, &sql); err != nil {
			t.Fatalf("Failed to scan: %v", err)
		}
	}
	rows.Close()

	_, err = conn.Exec(ctx, sql)
	if err == nil {
		t.Fatal("Expected error from failing planned command")
	}
	if !strings.Contains(err.Error(), "nonexistent_table_xyz") {
		t.Errorf("Expected error to mention table name, got: %v", err)
	}
}

func TestExecutePlannedCommands_ContextCancellation(t *testing.T) {
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
	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create unittest schema: %v", err)
	}

	_, err = conn.Exec(ctx, "SELECT pg_temp.pgmi_plan_command('SELECT pg_sleep(10);')")
	if err != nil {
		t.Fatalf("Failed to plan command: %v", err)
	}

	cancel()

	_, err = conn.Exec(ctx, "SELECT 1")
	if err == nil {
		t.Fatal("Expected error after context cancellation")
	}
}
