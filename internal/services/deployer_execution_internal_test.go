package services

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func requireTestDB(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	conn := os.Getenv("PGMI_TEST_CONN")
	if conn == "" {
		t.Skip("PGMI_TEST_CONN not set")
	}
	return conn
}

func createTestDB(t *testing.T, connString, dbName string) func() {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName)); err != nil {
		pool.Close()
		t.Fatalf("Failed to drop database: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", dbName)); err != nil {
		pool.Close()
		t.Fatalf("Failed to create database: %v", err)
	}
	pool.Close()
	return func() {
		p, err := pgxpool.New(ctx, connString)
		if err != nil {
			return
		}
		defer p.Close()
		_, _ = p.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
	}
}

func connectToTestDB(t *testing.T, connString, dbName string) *pgxpool.Pool {
	t.Helper()
	config, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatalf("Failed to parse connection string: %v", err)
	}
	config.Database = dbName
	pool, err := pgxpool.New(context.Background(), db.BuildConnectionString(config))
	if err != nil {
		t.Fatalf("Failed to connect to %s: %v", dbName, err)
	}
	return pool
}

func prepareSessionTables(t *testing.T, ctx context.Context, conn *pgxpool.Conn) {
	t.Helper()
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
}

func newServiceWithReadContent(content string) *DeploymentService {
	fs := &mockFileScanner{readContent: content}
	return NewDeploymentService(
		func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) { return &mockConnector{}, nil },
		&mockApprover{},
		&mockLogger{},
		&mockSessionPreparer{},
		fs,
		&mockDatabaseManager{},
	)
}

func TestExecuteDeploySQL_PopulatesPlan_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_deploy_plan"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	prepareSessionTables(t, ctx, conn)

	deploySQL := `
		SELECT pg_temp.pgmi_plan_command('SELECT 1;');
		SELECT pg_temp.pgmi_plan_command('SELECT 2;');
	`
	svc := newServiceWithReadContent(deploySQL)

	if err := svc.executeDeploySQL(ctx, conn, "/fake/path"); err != nil {
		t.Fatalf("executeDeploySQL failed: %v", err)
	}

	var planCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_plan").Scan(&planCount); err != nil {
		t.Fatalf("Failed to query plan: %v", err)
	}
	if planCount != 2 {
		t.Errorf("Expected 2 plan entries, got %d", planCount)
	}
}

func TestExecuteDeploySQL_SyntaxError_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_deploy_syn"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	prepareSessionTables(t, ctx, conn)

	svc := newServiceWithReadContent("SELCT INVALID SYNTAX;")

	err = svc.executeDeploySQL(ctx, conn, "/fake/path")
	if err == nil {
		t.Fatal("Expected error for invalid SQL")
	}
	if !strings.Contains(err.Error(), "deploy.sql execution failed") {
		t.Errorf("Expected deploy.sql error wrapper, got: %v", err)
	}
}

func TestExecuteDeploySQL_ReadError_Internal(t *testing.T) {
	fs := &mockFileScanner{readErr: pgmi.ErrDeploySQLNotFound}
	svc := NewDeploymentService(
		func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) { return &mockConnector{}, nil },
		&mockApprover{},
		&mockLogger{},
		&mockSessionPreparer{},
		fs,
		&mockDatabaseManager{},
	)

	err := svc.executeDeploySQL(context.Background(), nil, "/nonexistent")
	if err == nil {
		t.Fatal("Expected error for missing deploy.sql")
	}
	if !strings.Contains(err.Error(), "failed to read deploy.sql") {
		t.Errorf("Expected read error wrapper, got: %v", err)
	}
}

func TestExecutePlannedCommands_MultipleCommands_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_exec_multi"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	prepareSessionTables(t, ctx, conn)

	_, err = conn.Exec(ctx, `
		SELECT pg_temp.pgmi_plan_command('CREATE TABLE exec_t1(id int);');
		SELECT pg_temp.pgmi_plan_command('CREATE TABLE exec_t2(id int);');
		SELECT pg_temp.pgmi_plan_command('CREATE TABLE exec_t3(id int);');
	`)
	if err != nil {
		t.Fatalf("Failed to populate plan: %v", err)
	}

	svc := newServiceWithReadContent("")
	if err := svc.executePlannedCommands(ctx, conn); err != nil {
		t.Fatalf("executePlannedCommands failed: %v", err)
	}

	for _, table := range []string{"exec_t1", "exec_t2", "exec_t3"} {
		var exists bool
		if err := conn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)",
			table,
		).Scan(&exists); err != nil {
			t.Fatalf("Failed to check table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("Expected table %s to exist", table)
		}
	}
}

func TestExecutePlannedCommands_EmptyPlan_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_exec_empty"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	prepareSessionTables(t, ctx, conn)

	svc := newServiceWithReadContent("")
	if err := svc.executePlannedCommands(ctx, conn); err != nil {
		t.Fatalf("Expected success for empty plan, got: %v", err)
	}
}

func TestExecutePlannedCommands_FailingCommand_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_exec_fail"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	prepareSessionTables(t, ctx, conn)

	_, err = conn.Exec(ctx, `
		SELECT pg_temp.pgmi_plan_command('CREATE TABLE before_fail(id int);');
		SELECT pg_temp.pgmi_plan_command('SELECT * FROM nonexistent_table_xyz;');
	`)
	if err != nil {
		t.Fatalf("Failed to populate plan: %v", err)
	}

	svc := newServiceWithReadContent("")
	err = svc.executePlannedCommands(ctx, conn)
	if err == nil {
		t.Fatal("Expected error from failing command")
	}
	if !strings.Contains(err.Error(), "execution failed") {
		t.Errorf("Expected execution failed wrapper, got: %v", err)
	}
	if !strings.Contains(err.Error(), "nonexistent_table_xyz") {
		t.Errorf("Expected error preview with table name, got: %v", err)
	}
}

func TestExecutePlannedCommands_LongSQLTruncated_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_exec_trunc"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	prepareSessionTables(t, ctx, conn)

	longSQL := "SELECT * FROM " + strings.Repeat("x", 300) + ";"
	_, err = conn.Exec(ctx, "SELECT pg_temp.pgmi_plan_command($1)", longSQL)
	if err != nil {
		t.Fatalf("Failed to populate plan: %v", err)
	}

	svc := newServiceWithReadContent("")
	err = svc.executePlannedCommands(ctx, conn)
	if err == nil {
		t.Fatal("Expected error from failing command")
	}
	if !strings.Contains(err.Error(), "...") {
		t.Errorf("Expected truncated preview with '...', got: %v", err)
	}
}

func TestExecutePlannedCommands_ContextCancelled_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_exec_cancel"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	prepareSessionTables(t, ctx, conn)

	_, err = conn.Exec(ctx, "SELECT pg_temp.pgmi_plan_command('SELECT pg_sleep(10);')")
	if err != nil {
		t.Fatalf("Failed to populate plan: %v", err)
	}

	cancel()

	svc := newServiceWithReadContent("")
	err = svc.executePlannedCommands(ctx, conn)
	if err == nil {
		t.Fatal("Expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Errorf("Expected cancellation error, got: %v", err)
	}
}

func TestExecutePlannedCommands_OrderPreserved_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_exec_order"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	prepareSessionTables(t, ctx, conn)

	_, err = conn.Exec(ctx, `
		SELECT pg_temp.pgmi_plan_command('CREATE TABLE order_log(step int);');
		SELECT pg_temp.pgmi_plan_command('INSERT INTO order_log VALUES (1);');
		SELECT pg_temp.pgmi_plan_command('INSERT INTO order_log VALUES (2);');
		SELECT pg_temp.pgmi_plan_command('INSERT INTO order_log VALUES (3);');
	`)
	if err != nil {
		t.Fatalf("Failed to populate plan: %v", err)
	}

	svc := newServiceWithReadContent("")
	if err := svc.executePlannedCommands(ctx, conn); err != nil {
		t.Fatalf("executePlannedCommands failed: %v", err)
	}

	rows, err := conn.Query(ctx, "SELECT step FROM order_log ORDER BY step")
	if err != nil {
		t.Fatalf("Failed to query order log: %v", err)
	}
	var steps []int
	for rows.Next() {
		var step int
		if err := rows.Scan(&step); err != nil {
			t.Fatalf("Failed to scan: %v", err)
		}
		steps = append(steps, step)
	}
	rows.Close()

	if len(steps) != 3 {
		t.Fatalf("Expected 3 steps, got %d", len(steps))
	}
	for i, step := range steps {
		if step != i+1 {
			t.Errorf("Expected step %d at position %d, got %d", i+1, i, step)
		}
	}
}
