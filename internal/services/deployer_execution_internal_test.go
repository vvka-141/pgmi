package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/internal/testinfra"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var (
	execContainerOnce sync.Once
	execContainerConn string
	execContainerErr  error
)

func requireTestDB(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	if conn := os.Getenv("PGMI_TEST_CONN"); conn != "" {
		return conn
	}
	execContainerOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				execContainerErr = fmt.Errorf("testcontainer startup panicked: %v", r)
			}
		}()
		container, err := testinfra.StartSimplePostgres(context.Background())
		if err != nil {
			execContainerErr = err
			return
		}
		execContainerConn = container.ConnString
	})
	if execContainerErr == nil {
		return execContainerConn
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PGMI_REQUIRE_DB"))) {
	case "", "0", "false", "no":
		t.Skipf("PGMI_TEST_CONN not set and Docker unavailable: %v", execContainerErr)
	default:
		t.Fatalf("PGMI_REQUIRE_DB is set but no test database is available: %v", execContainerErr)
	}
	return ""
}

func createTestDB(t *testing.T, connString, dbName string) func() {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", pgx.Identifier{dbName}.Sanitize())); err != nil {
		pool.Close()
		t.Fatalf("Failed to drop database: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{dbName}.Sanitize())); err != nil {
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
		_, _ = p.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", pgx.Identifier{dbName}.Sanitize()))
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
}

func newServiceWithReadContent(content string) *DeploymentService {
	fs := &mockFileScanner{readContent: content}
	svc := NewDeploymentService(
		func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) { return &mockConnector{}, nil },
		&mockApprover{},
		&mockLogger{},
		&mockSessionPreparer{},
		fs,
		&mockDatabaseManager{},
	)
	// Deploy() allocates this before it reaches executeDeploySQL, which records
	// unit counts into it. Calling the unexported method directly skips that.
	svc.lastResult = &DeployResult{}
	return svc
}

func TestExecuteDeploySQL_DirectExecution_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_deploy_direct"
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
		CREATE TABLE direct_exec_test(id int);
		INSERT INTO direct_exec_test VALUES (1), (2), (3);
	`
	svc := newServiceWithReadContent(deploySQL)

	if _, err := svc.executeDeploySQL(ctx, conn, "/fake/path"); err != nil {
		t.Fatalf("executeDeploySQL failed: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM direct_exec_test").Scan(&count); err != nil {
		t.Fatalf("Failed to query table: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 rows, got %d", count)
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

	_, err = svc.executeDeploySQL(ctx, conn, "/fake/path")
	if err == nil {
		t.Fatal("Expected error for invalid SQL")
	}
	if !errors.Is(err, pgmi.ErrExecutionFailed) {
		t.Errorf("Expected ErrExecutionFailed sentinel, got: %v", err)
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

	_, err := svc.executeDeploySQL(context.Background(), nil, "/nonexistent")
	if err == nil {
		t.Fatal("Expected error for missing deploy.sql")
	}
	if !errors.Is(err, pgmi.ErrDeploySQLNotFound) {
		t.Errorf("Expected ErrDeploySQLNotFound sentinel, got: %v", err)
	}
}
