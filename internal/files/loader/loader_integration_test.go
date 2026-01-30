package loader_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/params"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestLoadFilesIntoSession_Basic(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_basic"
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
		{Path: "./migrations/001.sql", Content: "CREATE TABLE t1(id int);", Checksum: "a000000000000000000000000000000000000000000000000000000000000001", ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000001"},
		{Path: "./migrations/002.sql", Content: "CREATE TABLE t2(id int);", Checksum: "a000000000000000000000000000000000000000000000000000000000000002", ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000002"},
	}

	if err := l.LoadFilesIntoSession(ctx, conn, files); err != nil {
		t.Fatalf("LoadFilesIntoSession failed: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_source").Scan(&count); err != nil {
		t.Fatalf("Failed to query pgmi_source: %v", err)
	}

	if count != 2 {
		t.Errorf("Expected 2 files, got %d", count)
	}
}

func TestLoadFilesIntoSession_Empty(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_empty"
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
		t.Fatalf("LoadFilesIntoSession with empty files failed: %v", err)
	}
}

func TestLoadParametersIntoSession_Valid(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_params"
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
	p := map[string]string{"env": "test", "version": "1.0"}

	if err := l.LoadParametersIntoSession(ctx, conn, p); err != nil {
		t.Fatalf("LoadParametersIntoSession failed: %v", err)
	}

	var val string
	if err := conn.QueryRow(ctx, "SELECT current_setting('pgmi.env')").Scan(&val); err != nil {
		t.Fatalf("Failed to read session variable: %v", err)
	}

	if val != "test" {
		t.Errorf("Expected 'test', got %q", val)
	}
}

func TestLoadParametersIntoSession_Empty(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_params_empty"
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
	if err := l.LoadParametersIntoSession(ctx, conn, map[string]string{}); err != nil {
		t.Fatalf("LoadParametersIntoSession with empty params failed: %v", err)
	}
}

func TestLoadParametersIntoSession_InvalidKey(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_invalid_key"
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
	p := map[string]string{"invalid key": "value"}

	err = l.LoadParametersIntoSession(ctx, conn, p)
	if err == nil {
		t.Fatal("Expected error for invalid key with spaces")
	}
}

func TestLoadParametersIntoSession_KeyTooLong(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_long_key"
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
	longKey := ""
	for i := 0; i < 64; i++ {
		longKey += "a"
	}
	p := map[string]string{longKey: "value"}

	err = l.LoadParametersIntoSession(ctx, conn, p)
	if err == nil {
		t.Fatal("Expected error for key exceeding 63 chars")
	}
}

func TestLoadFilesIntoSession_WithMetadata(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_metadata"
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

	testUUID := "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	files := []pgmi.FileMetadata{
		{
			Path:        "./setup/functions.sql",
			Content:     "CREATE FUNCTION f();",
			Checksum:    "a000000000000000000000000000000000000000000000000000000000000003",
			ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000003",
			Metadata: &pgmi.ScriptMetadata{
				ID:          uuid.MustParse(testUUID),
				Idempotent:  true,
				SortKeys:    []string{"10-setup/0010"},
				Description: "Test function",
			},
		},
	}

	if err := l.LoadFilesIntoSession(ctx, conn, files); err != nil {
		t.Fatalf("LoadFilesIntoSession failed: %v", err)
	}

	var metaCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_source_metadata").Scan(&metaCount); err != nil {
		t.Fatalf("Failed to query metadata: %v", err)
	}

	if metaCount != 1 {
		t.Errorf("Expected 1 metadata row, got %d", metaCount)
	}
}
