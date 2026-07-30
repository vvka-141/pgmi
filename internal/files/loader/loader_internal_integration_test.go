package loader

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/internal/testinfra"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var (
	loaderContainerOnce sync.Once
	loaderContainerConn string
	loaderContainerErr  error
)

func requireTestDB(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	if conn := os.Getenv("PGMI_TEST_CONN"); conn != "" {
		return conn
	}
	loaderContainerOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				loaderContainerErr = fmt.Errorf("testcontainer startup panicked: %v", r)
			}
		}()
		container, err := testinfra.StartSimplePostgres(context.Background())
		if err != nil {
			loaderContainerErr = err
			return
		}
		loaderContainerConn = container.ConnString
	})
	if loaderContainerErr == nil {
		return loaderContainerConn
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PGMI_REQUIRE_DB"))) {
	case "", "0", "false", "no":
		t.Skipf("PGMI_TEST_CONN not set and Docker unavailable: %v", loaderContainerErr)
	default:
		t.Fatalf("PGMI_REQUIRE_DB is set but no test database is available: %v", loaderContainerErr)
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

func acquireWithSchema(t *testing.T, pool *pgxpool.Pool) *pgxpool.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	if err := params.CreateSchema(ctx, conn); err != nil {
		conn.Release()
		t.Fatalf("Failed to create schema: %v", err)
	}
	return conn
}

func TestInsertFiles_BatchExecution_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_files"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	files := []pgmi.FileMetadata{
		{Path: "./migrations/001.sql", Content: "CREATE TABLE t1(id int);", Checksum: "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1", ChecksumRaw: "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1"},
		{Path: "./migrations/002.sql", Content: "CREATE TABLE t2(id int);", Checksum: "a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2", ChecksumRaw: "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"},
		{Path: "./setup/init.sql", Content: "SELECT 1;", Checksum: "a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3", ChecksumRaw: "b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3"},
	}

	if err := l.insertFiles(ctx, conn, files); err != nil {
		t.Fatalf("insertFiles failed: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_source").Scan(&count); err != nil {
		t.Fatalf("Failed to query pgmi_source: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 files, got %d", count)
	}
}

func TestInsertFiles_ManyFiles_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_many"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	files := make([]pgmi.FileMetadata, 50)
	for i := range files {
		files[i] = pgmi.FileMetadata{
			Path:        fmt.Sprintf("./scripts/%03d.sql", i),
			Content:     fmt.Sprintf("SELECT %d;", i),
			Checksum:    fmt.Sprintf("%064x", i),
			ChecksumRaw: fmt.Sprintf("%064x", i+1000),
		}
	}

	if err := l.insertFiles(ctx, conn, files); err != nil {
		t.Fatalf("insertFiles with 50 files failed: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_source").Scan(&count); err != nil {
		t.Fatalf("Failed to query pgmi_source: %v", err)
	}
	if count != 50 {
		t.Errorf("Expected 50 files, got %d", count)
	}
}

func TestInsertParams_BatchExecution_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_params"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	p := map[string]string{
		"env":     "staging",
		"version": "2.0",
		"debug":   "true",
	}

	if err := l.insertParams(ctx, conn, p); err != nil {
		t.Fatalf("insertParams failed: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_parameter").Scan(&count); err != nil {
		t.Fatalf("Failed to query pgmi_parameter: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 parameters, got %d", count)
	}

	var val string
	if err := conn.QueryRow(ctx, "SELECT value FROM pg_temp._pgmi_parameter WHERE key = 'env'").Scan(&val); err != nil {
		t.Fatalf("Failed to query env parameter: %v", err)
	}
	if val != "staging" {
		t.Errorf("Expected 'staging', got %q", val)
	}
}

func TestInsertParams_SpecialValues_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_special"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	p := map[string]string{
		"url":       "https://example.com/path?q=1&b=2",
		"json":      `{"key": "value", "num": 42}`,
		"multiline": "line1\nline2\nline3",
		"empty":     "",
		"unicode":   "日本語テスト",
	}

	if err := l.insertParams(ctx, conn, p); err != nil {
		t.Fatalf("insertParams with special values failed: %v", err)
	}

	checks := map[string]string{
		"url":       "https://example.com/path?q=1&b=2",
		"json":      `{"key": "value", "num": 42}`,
		"multiline": "line1\nline2\nline3",
		"empty":     "",
		"unicode":   "日本語テスト",
	}

	for key, expected := range checks {
		var val string
		if err := conn.QueryRow(ctx, "SELECT value FROM pg_temp._pgmi_parameter WHERE key = $1", key).Scan(&val); err != nil {
			t.Fatalf("Failed to query parameter %s: %v", key, err)
		}
		if val != expected {
			t.Errorf("Parameter %s: expected %q, got %q", key, expected, val)
		}
	}
}

func TestSetSessionVariables_BatchExecution_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_sessvars"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	p := map[string]string{
		"env":         "production",
		"app_version": "3.1.0",
		"region":      "eu_west_1",
	}

	if err := l.setSessionVariables(ctx, conn, p); err != nil {
		t.Fatalf("setSessionVariables failed: %v", err)
	}

	checks := map[string]string{
		"pgmi.env":         "production",
		"pgmi.app_version": "3.1.0",
		"pgmi.region":      "eu_west_1",
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
}

func TestSetSessionVariables_CaseNormalization_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_case"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	p := map[string]string{"MyParam": "value1", "ALL_UPPER": "value2"}

	if err := l.setSessionVariables(ctx, conn, p); err != nil {
		t.Fatalf("setSessionVariables failed: %v", err)
	}

	var val string
	if err := conn.QueryRow(ctx, "SELECT current_setting('pgmi.myparam')").Scan(&val); err != nil {
		t.Fatalf("Failed to read lowercased session var: %v", err)
	}
	if val != "value1" {
		t.Errorf("Expected 'value1', got %q", val)
	}
}

func TestInsertMetadata_BatchExecution_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_meta"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	testUUID1 := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11")
	testUUID2 := uuid.MustParse("b0eebc99-9c0b-4ef8-bb6d-6bb9bd380a22")

	files := []pgmi.FileMetadata{
		{
			Path: "./setup/001.sql", Content: "CREATE TABLE t1(id int);",
			Checksum:    "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1",
			ChecksumRaw: "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1",
		},
		{
			Path: "./setup/funcs.sql", Content: "CREATE FUNCTION f();",
			Checksum:    "a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2",
			ChecksumRaw: "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2",
			Metadata: &pgmi.ScriptMetadata{
				ID: testUUID1, Idempotent: true,
				SortKeys: []string{"10-setup/0010"}, Description: "Setup functions",
			},
		},
		{
			Path: "./migrations/002.sql", Content: "ALTER TABLE t1 ADD col text;",
			Checksum:    "a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3",
			ChecksumRaw: "b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3",
			Metadata: &pgmi.ScriptMetadata{
				ID: testUUID2, Idempotent: false,
				SortKeys: []string{"20-migrate/0020"}, Description: "Add column",
			},
		},
	}

	if err := l.insertFiles(ctx, conn, files); err != nil {
		t.Fatalf("insertFiles failed: %v", err)
	}
	if err := l.insertMetadata(ctx, conn, files); err != nil {
		t.Fatalf("insertMetadata failed: %v", err)
	}

	var metaCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_source_metadata").Scan(&metaCount); err != nil {
		t.Fatalf("Failed to query metadata count: %v", err)
	}
	if metaCount != 2 {
		t.Errorf("Expected 2 metadata rows, got %d", metaCount)
	}
}

func TestInsertMetadata_FieldVerification_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_metafields"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	testUUID := uuid.MustParse("c0eebc99-9c0b-4ef8-bb6d-6bb9bd380a33")
	files := []pgmi.FileMetadata{
		{
			Path: "./lib/core/init.sql", Content: "SELECT 1;",
			Checksum:    "d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1",
			ChecksumRaw: "e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1",
			Metadata: &pgmi.ScriptMetadata{
				ID: testUUID, Idempotent: true,
				SortKeys:    []string{"05-core/0001", "10-lib/0001"},
				Description: "Core initialization",
			},
		},
	}

	if err := l.insertFiles(ctx, conn, files); err != nil {
		t.Fatalf("insertFiles failed: %v", err)
	}
	if err := l.insertMetadata(ctx, conn, files); err != nil {
		t.Fatalf("insertMetadata failed: %v", err)
	}

	var storedPath, storedID, storedDesc string
	var storedIdempotent bool
	var storedSortKeys []string

	err := conn.QueryRow(ctx, `
		SELECT path, id, idempotent, sort_keys, description
		FROM pg_temp._pgmi_source_metadata
		WHERE path = './lib/core/init.sql'
	`).Scan(&storedPath, &storedID, &storedIdempotent, &storedSortKeys, &storedDesc)
	if err != nil {
		t.Fatalf("Failed to query metadata fields: %v", err)
	}

	if storedPath != "./lib/core/init.sql" {
		t.Errorf("Path mismatch: got %q", storedPath)
	}
	if storedID != testUUID.String() {
		t.Errorf("ID mismatch: expected %q, got %q", testUUID.String(), storedID)
	}
	if !storedIdempotent {
		t.Error("Expected idempotent=true")
	}
	if len(storedSortKeys) != 2 || storedSortKeys[0] != "05-core/0001" || storedSortKeys[1] != "10-lib/0001" {
		t.Errorf("SortKeys mismatch: got %v", storedSortKeys)
	}
	if storedDesc != "Core initialization" {
		t.Errorf("Description mismatch: got %q", storedDesc)
	}
}

func TestLoadFilesIntoSession_FullPath_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_fullfiles"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	testUUID := uuid.MustParse("d0eebc99-9c0b-4ef8-bb6d-6bb9bd380a44")
	files := []pgmi.FileMetadata{
		{
			Path: "./a.sql", Content: "SELECT 1;",
			Checksum:    "f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1",
			ChecksumRaw: "f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2",
			Metadata: &pgmi.ScriptMetadata{
				ID: testUUID, Idempotent: false, SortKeys: []string{"01/0001"},
				Description: "First file",
			},
		},
		{
			Path: "./b.sql", Content: "SELECT 2;",
			Checksum:    "f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3",
			ChecksumRaw: "f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4f4",
		},
	}

	if err := l.LoadFilesIntoSession(ctx, conn, files); err != nil {
		t.Fatalf("LoadFilesIntoSession failed: %v", err)
	}

	var fileCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_source").Scan(&fileCount); err != nil {
		t.Fatalf("Failed to query file count: %v", err)
	}
	if fileCount != 2 {
		t.Errorf("Expected 2 files, got %d", fileCount)
	}

	var metaCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_source_metadata").Scan(&metaCount); err != nil {
		t.Fatalf("Failed to query metadata count: %v", err)
	}
	if metaCount != 1 {
		t.Errorf("Expected 1 metadata row, got %d", metaCount)
	}
}

func TestLoadParametersIntoSession_FullPath_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_fullparams"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	p := map[string]string{"env": "test", "timeout": "30"}

	if err := l.LoadParametersIntoSession(ctx, conn, p); err != nil {
		t.Fatalf("LoadParametersIntoSession failed: %v", err)
	}

	var paramCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp._pgmi_parameter").Scan(&paramCount); err != nil {
		t.Fatalf("Failed to query parameter count: %v", err)
	}
	if paramCount != 2 {
		t.Errorf("Expected 2 parameters, got %d", paramCount)
	}

	var val string
	if err := conn.QueryRow(ctx, "SELECT current_setting('pgmi.env')").Scan(&val); err != nil {
		t.Fatalf("Failed to read session variable: %v", err)
	}
	if val != "test" {
		t.Errorf("Expected 'test', got %q", val)
	}
}

func TestLoadParametersIntoSession_InvalidKeyError_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_badkey"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	err := l.LoadParametersIntoSession(ctx, conn, map[string]string{"bad key": "val"})
	if err == nil {
		t.Fatal("Expected error for invalid key")
	}
	if !strings.Contains(err.Error(), "invalid parameter key") {
		t.Errorf("Expected invalid parameter key error, got: %v", err)
	}
}

func TestInsertFiles_ContentPreservation_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_content"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	largeContent := strings.Repeat("-- line\n", 5000)
	files := []pgmi.FileMetadata{
		{
			Path: "./large.sql", Content: largeContent,
			Checksum:    "1111111111111111111111111111111111111111111111111111111111111111",
			ChecksumRaw: "2222222222222222222222222222222222222222222222222222222222222222",
		},
	}

	if err := l.insertFiles(ctx, conn, files); err != nil {
		t.Fatalf("insertFiles failed: %v", err)
	}

	var stored string
	if err := conn.QueryRow(ctx, "SELECT content FROM pg_temp._pgmi_source WHERE path = './large.sql'").Scan(&stored); err != nil {
		t.Fatalf("Failed to query content: %v", err)
	}
	if stored != largeContent {
		t.Errorf("Content mismatch: expected %d bytes, got %d bytes", len(largeContent), len(stored))
	}
}

// sortKeys is optional in <pgmi-meta>. Absent, it parses to a nil slice, and
// pgx sends a nil slice as SQL NULL — which _pgmi_source_metadata.sort_keys
// rejects, failing the whole deploy with a raw 23502 before any SQL ran:
//
//	failed to insert metadata for file ./m/f.sql: ERROR: null value in column
//	"sort_keys" ... violates not-null constraint (SQLSTATE 23502)
//
// The column defaults to '{}' precisely for this, and pgmi_plan_view's
// NULLIF(m.sort_keys, '{}') fallback is written for it: a file with metadata
// but no sort keys orders by its own path. NULL defeated both.
//
// Reproduced against PG 17.10 with a minimal id-only metadata block — the
// documented way to give a script a stable identity without pinning its order.
func TestInsertMetadata_AbsentSortKeysStoresEmptyArray(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_nosortkeys"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	for _, tc := range []struct {
		name     string
		sortKeys []string
	}{
		{"no sortKeys element at all", nil},
		{"empty sortKeys element", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "./m/" + strings.ReplaceAll(tc.name, " ", "_") + ".sql"
			files := []pgmi.FileMetadata{{
				Path: path, Content: "SELECT 1;",
				Checksum:    "d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2",
				ChecksumRaw: "e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2",
				Metadata: &pgmi.ScriptMetadata{
					ID: uuid.New(), Idempotent: true,
					SortKeys:    tc.sortKeys,
					Description: "identity only",
				},
			}}

			if err := l.insertFiles(ctx, conn, files); err != nil {
				t.Fatalf("insertFiles: %v", err)
			}
			if err := l.insertMetadata(ctx, conn, files); err != nil {
				t.Fatalf("insertMetadata rejected metadata without sort keys: %v", err)
			}

			var stored []string
			if err := conn.QueryRow(ctx,
				`SELECT sort_keys FROM pg_temp._pgmi_source_metadata WHERE path = $1`,
				path).Scan(&stored); err != nil {
				t.Fatalf("read back sort_keys: %v", err)
			}
			if stored == nil {
				t.Error("sort_keys stored as NULL; the plan view's NULLIF fallback needs '{}'")
			}
			if len(stored) != 0 {
				t.Errorf("sort_keys = %v, want empty", stored)
			}
		})
	}
}

// Autovacuum never sees a temp table, so unless loading analyzes them the
// planner builds every deploy.sql query on default estimates. Measured on a
// 20-directory project: pgmi_test_plan took 546ms per call without statistics
// and 1.8ms with them, on every deploy.
func TestLoadFilesIntoSession_AnalyzesSessionTables_Internal(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_ldr_analyze"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	conn := acquireWithSchema(t, pool)
	defer conn.Release()

	ctx := context.Background()
	l := NewLoader()

	files := []pgmi.FileMetadata{
		{
			Path: "./a.sql", Content: "SELECT 1;", Extension: ".sql",
			Checksum:    "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1",
			ChecksumRaw: "a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2",
		},
		{
			Path: "./__test__/test_a.sql", Content: "SELECT 1;", Extension: ".sql",
			Checksum:    "a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3",
			ChecksumRaw: "a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4",
		},
	}

	if err := l.LoadFilesIntoSession(ctx, conn, files); err != nil {
		t.Fatalf("LoadFilesIntoSession failed: %v", err)
	}

	// pg_stats holds a row per analyzed column, and nothing at all for a table
	// the planner has no statistics for.
	for _, table := range []string{"_pgmi_source", "_pgmi_test_source", "_pgmi_test_directory"} {
		var cols int
		err := conn.QueryRow(ctx, `
			SELECT count(*) FROM pg_stats
			WHERE tablename = $1
			  AND schemaname = (SELECT nspname FROM pg_namespace
			                    WHERE oid = pg_my_temp_schema())`, table).Scan(&cols)
		if err != nil {
			t.Fatalf("querying pg_stats for %s: %v", table, err)
		}
		// ANALYZE produces no pg_stats rows for an empty table, so an
		// incomplete fixture would make the check above pass vacuously.
		var rows int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_temp."+table).Scan(&rows); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if rows == 0 {
			t.Fatalf("%s is empty, so this test proves nothing about it", table)
		}
		if cols == 0 {
			t.Errorf("%s has %d rows but no planner statistics after loading", table, rows)
		}
	}
}
