package loader_test

import (
	"context"
	"strings"
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
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_source").Scan(&count); err != nil {
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
	longKey := strings.Repeat("a", 64)
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
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_source_metadata").Scan(&metaCount); err != nil {
		t.Fatalf("Failed to query metadata: %v", err)
	}

	if metaCount != 1 {
		t.Errorf("Expected 1 metadata row, got %d", metaCount)
	}
}

func TestLoadFilesIntoSession_VerifyContent(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_content"
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
			Path:        "./migrations/001_users.sql",
			Content:     "CREATE TABLE users (id serial PRIMARY KEY);",
			Checksum:    "aaaa000000000000000000000000000000000000000000000000000000000001",
			ChecksumRaw: "bbbb000000000000000000000000000000000000000000000000000000000001",
		},
	}

	if err := l.LoadFilesIntoSession(ctx, conn, files); err != nil {
		t.Fatalf("LoadFilesIntoSession failed: %v", err)
	}

	var path, name, directory, content, checksumRaw, checksumNorm string
	err = conn.QueryRow(ctx, `
		SELECT path, name, directory, content, checksum, pgmi_checksum
		FROM pg_temp._pgmi_source
		WHERE path = './migrations/001_users.sql'
	`).Scan(&path, &name, &directory, &content, &checksumRaw, &checksumNorm)
	if err != nil {
		t.Fatalf("Failed to query file details: %v", err)
	}

	if path != "./migrations/001_users.sql" {
		t.Errorf("Expected path './migrations/001_users.sql', got %q", path)
	}
	if name != "001_users.sql" {
		t.Errorf("Expected name '001_users.sql', got %q", name)
	}
	if !strings.Contains(directory, "migrations") {
		t.Errorf("Expected directory containing 'migrations', got %q", directory)
	}
	if content != "CREATE TABLE users (id serial PRIMARY KEY);" {
		t.Errorf("Content mismatch: got %q", content)
	}
	if checksumRaw != "bbbb000000000000000000000000000000000000000000000000000000000001" {
		t.Errorf("Raw checksum mismatch: got %q", checksumRaw)
	}
	if checksumNorm != "aaaa000000000000000000000000000000000000000000000000000000000001" {
		t.Errorf("Normalized checksum mismatch: got %q", checksumNorm)
	}
}

func TestLoadFilesIntoSession_WithAndWithoutMetadata(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_mixed_meta"
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
			Content:     "CREATE TABLE t1(id int);",
			Checksum:    "a000000000000000000000000000000000000000000000000000000000000001",
			ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000001",
		},
		{
			Path:        "./setup/funcs.sql",
			Content:     "CREATE FUNCTION f1();",
			Checksum:    "a000000000000000000000000000000000000000000000000000000000000002",
			ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000002",
			Metadata: &pgmi.ScriptMetadata{
				ID:          uuid.MustParse("b0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"),
				Idempotent:  true,
				SortKeys:    []string{"20-setup/0010"},
				Description: "Setup function",
			},
		},
		{
			Path:        "./migrations/002.sql",
			Content:     "CREATE TABLE t2(id int);",
			Checksum:    "a000000000000000000000000000000000000000000000000000000000000003",
			ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000003",
		},
	}

	if err := l.LoadFilesIntoSession(ctx, conn, files); err != nil {
		t.Fatalf("LoadFilesIntoSession failed: %v", err)
	}

	var fileCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_source").Scan(&fileCount); err != nil {
		t.Fatalf("Failed to query file count: %v", err)
	}
	if fileCount != 3 {
		t.Errorf("Expected 3 files, got %d", fileCount)
	}

	var metaCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_source_metadata").Scan(&metaCount); err != nil {
		t.Fatalf("Failed to query metadata count: %v", err)
	}
	if metaCount != 1 {
		t.Errorf("Expected 1 metadata row, got %d", metaCount)
	}
}

func TestLoadParametersIntoSession_VerifySessionVars(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_verify_vars"
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
	p := map[string]string{
		"env":         "staging",
		"app_version": "2.5.0",
		"debug":       "true",
	}

	if err := l.LoadParametersIntoSession(ctx, conn, p); err != nil {
		t.Fatalf("LoadParametersIntoSession failed: %v", err)
	}

	checks := map[string]string{
		"pgmi.env":         "staging",
		"pgmi.app_version": "2.5.0",
		"pgmi.debug":       "true",
	}

	for varName, expected := range checks {
		var val string
		if err := conn.QueryRow(ctx, "SELECT current_setting($1)", varName).Scan(&val); err != nil {
			t.Fatalf("Failed to read session variable %s: %v", varName, err)
		}
		if val != expected {
			t.Errorf("Session var %s: expected %q, got %q", varName, expected, val)
		}
	}

	var paramCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_parameter").Scan(&paramCount); err != nil {
		t.Fatalf("Failed to count parameters: %v", err)
	}
	if paramCount != 3 {
		t.Errorf("Expected 3 parameters in table, got %d", paramCount)
	}
}

func TestLoadParametersIntoSession_CaseNormalization(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_case_norm"
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
	p := map[string]string{
		"MyParam":   "value1",
		"ALL_UPPER": "value2",
	}

	if err := l.LoadParametersIntoSession(ctx, conn, p); err != nil {
		t.Fatalf("LoadParametersIntoSession failed: %v", err)
	}

	var val1, val2 string
	if err := conn.QueryRow(ctx, "SELECT value FROM pg_temp._pgmi_parameter WHERE key = 'myparam'").Scan(&val1); err != nil {
		t.Fatalf("Failed to query lowercased key 'myparam': %v", err)
	}
	if val1 != "value1" {
		t.Errorf("Expected 'value1' for lowercased key, got %q", val1)
	}

	if err := conn.QueryRow(ctx, "SELECT current_setting('pgmi.myparam')").Scan(&val2); err != nil {
		t.Fatalf("Failed to read session variable pgmi.myparam: %v", err)
	}
	if val2 != "value1" {
		t.Errorf("Expected session var 'value1', got %q", val2)
	}
}

func TestLoadFilesIntoSession_RootLevelTestFiles(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_root_test"
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
		{Path: "./migrations/001_users.sql", Extension: ".sql", Content: "CREATE TABLE users();", Checksum: "a000000000000000000000000000000000000000000000000000000000000001", ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000001"},
		{Path: "./__test__/_setup.sql", Extension: ".sql", Content: "INSERT INTO users VALUES (1);", Checksum: "a000000000000000000000000000000000000000000000000000000000000002", ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000002"},
		{Path: "./__test__/test_users.sql", Extension: ".sql", Content: "SELECT * FROM users;", Checksum: "a000000000000000000000000000000000000000000000000000000000000003", ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000003"},
		{Path: "./schema/__test__/nested_test.sql", Extension: ".sql", Content: "SELECT 1;", Checksum: "a000000000000000000000000000000000000000000000000000000000000004", ChecksumRaw: "b000000000000000000000000000000000000000000000000000000000000004"},
	}

	if err := l.LoadFilesIntoSession(ctx, conn, files); err != nil {
		t.Fatalf("LoadFilesIntoSession failed: %v", err)
	}

	// Verify non-test files are in pgmi_source
	var sourceCount int
	err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_source").Scan(&sourceCount)
	if err != nil {
		t.Fatalf("Failed to count pgmi_source: %v", err)
	}
	if sourceCount != 1 {
		t.Errorf("Expected 1 file in pgmi_source, got %d", sourceCount)
	}

	// Verify test files are in pgmi_test_source
	var testCount int
	err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_test_source").Scan(&testCount)
	if err != nil {
		t.Fatalf("Failed to count pgmi_test_source: %v", err)
	}
	if testCount != 3 {
		t.Errorf("Expected 3 files in pgmi_test_source, got %d", testCount)
	}

	// Verify test file paths in pgmi_test_source
	testPaths := []string{"./__test__/_setup.sql", "./__test__/test_users.sql", "./schema/__test__/nested_test.sql"}
	for _, path := range testPaths {
		var exists bool
		err := conn.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM pg_temp._pgmi_test_source WHERE path = $1)",
			path,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("Failed to check test file %s: %v", path, err)
		}
		if !exists {
			t.Errorf("Test file %s not found in pgmi_test_source", path)
		}
	}
}

func TestLoadFilesIntoSession_LargeContent(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_loader_large"
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

	largeContent := strings.Repeat("-- This is a large SQL file line\n", 10000)

	l := loader.NewLoader()
	files := []pgmi.FileMetadata{
		{
			Path:        "./migrations/large.sql",
			Content:     largeContent,
			Checksum:    "c000000000000000000000000000000000000000000000000000000000000001",
			ChecksumRaw: "d000000000000000000000000000000000000000000000000000000000000001",
		},
	}

	if err := l.LoadFilesIntoSession(ctx, conn, files); err != nil {
		t.Fatalf("LoadFilesIntoSession failed for large file: %v", err)
	}

	var storedContent string
	if err := conn.QueryRow(ctx, "SELECT content FROM pg_temp._pgmi_source WHERE path = './migrations/large.sql'").Scan(&storedContent); err != nil {
		t.Fatalf("Failed to query large file content: %v", err)
	}
	if storedContent != largeContent {
		t.Errorf("Large file content mismatch: expected %d bytes, got %d bytes", len(largeContent), len(storedContent))
	}
}
