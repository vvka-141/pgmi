package services_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/logging"
	"github.com/vvka-141/pgmi/internal/services"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/internal/testing/fixtures"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestPrepareSession_FullLifecycle(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_session_lifecycle"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	connConfig, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatalf("Failed to parse connection string: %v", err)
	}
	connConfig.Database = testDB

	fs := fixtures.StandardMultiLevel()
	fileScanner := scanner.NewScannerWithFS(checksum.New(), fs)
	fileLoader := loader.NewLoader()
	logger := logging.NewNullLogger()

	sm := services.NewSessionManager(db.NewConnector, fileScanner, fileLoader, logger)

	session, err := sm.PrepareSession(ctx, connConfig, "/", map[string]string{"env": "test"}, false)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}
	defer session.Close()

	if session.Pool() == nil {
		t.Error("Expected non-nil pool")
	}
	if session.Conn() == nil {
		t.Error("Expected non-nil conn")
	}

	var count int
	if err := session.Conn().QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_source").Scan(&count); err != nil {
		t.Fatalf("Failed to query pgmi_source: %v", err)
	}
	if count == 0 {
		t.Error("Expected files in pgmi_source")
	}
}

func TestPrepareSession_VerboseMode(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_session_verbose"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	connConfig, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatalf("Failed to parse connection string: %v", err)
	}
	connConfig.Database = testDB

	fs := fixtures.EmptyProject()
	fileScanner := scanner.NewScannerWithFS(checksum.New(), fs)
	fileLoader := loader.NewLoader()
	logger := logging.NewNullLogger()

	sm := services.NewSessionManager(db.NewConnector, fileScanner, fileLoader, logger)

	session, err := sm.PrepareSession(ctx, connConfig, "/", nil, true)
	if err != nil {
		t.Fatalf("PrepareSession with verbose failed: %v", err)
	}
	defer session.Close()

	var msgLevel string
	if err := session.Conn().QueryRow(ctx, "SHOW client_min_messages").Scan(&msgLevel); err != nil {
		t.Fatalf("Failed to check client_min_messages: %v", err)
	}
	if !strings.HasPrefix(msgLevel, "debug") {
		t.Errorf("Expected debug-level client_min_messages, got %q", msgLevel)
	}
}

func TestPrepareSession_FilesAndParamsLoaded(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_session_loaded"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	connConfig, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatalf("Failed to parse connection string: %v", err)
	}
	connConfig.Database = testDB

	fs := fixtures.OnlyMigrations()
	fileScanner := scanner.NewScannerWithFS(checksum.New(), fs)
	fileLoader := loader.NewLoader()
	logger := logging.NewNullLogger()

	sm := services.NewSessionManager(db.NewConnector, fileScanner, fileLoader, logger)

	params := map[string]string{"env": "staging", "version": "3.0"}
	session, err := sm.PrepareSession(ctx, connConfig, "/", params, false)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}
	defer session.Close()

	var fileCount int
	if err := session.Conn().QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_source").Scan(&fileCount); err != nil {
		t.Fatalf("Failed to count files: %v", err)
	}
	if fileCount != 2 {
		t.Errorf("Expected 2 files, got %d", fileCount)
	}

	var paramCount int
	if err := session.Conn().QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_parameter").Scan(&paramCount); err != nil {
		t.Fatalf("Failed to count params: %v", err)
	}
	if paramCount != 2 {
		t.Errorf("Expected 2 params, got %d", paramCount)
	}

	var envVal string
	if err := session.Conn().QueryRow(ctx, "SELECT current_setting('pgmi.env')").Scan(&envVal); err != nil {
		t.Fatalf("Failed to read session variable: %v", err)
	}
	if envVal != "staging" {
		t.Errorf("Expected env='staging', got %q", envVal)
	}
}

func TestPrepareSession_CleanupOnError(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	connConfig := &pgmi.ConnectionConfig{
		Host:     "127.0.0.1",
		Port:     1, // invalid port
		Username: "invalid",
		Password: "invalid",
		Database: "nonexistent",
		SSLMode:  "disable",
	}

	fs := fixtures.EmptyProject()
	fileScanner := scanner.NewScannerWithFS(checksum.New(), fs)
	fileLoader := loader.NewLoader()
	logger := logging.NewNullLogger()

	sm := services.NewSessionManager(db.NewConnector, fileScanner, fileLoader, logger)

	_, err := sm.PrepareSession(context.Background(), connConfig, "/", nil, false)
	if err == nil {
		t.Fatal("Expected error for invalid connection")
	}

	_ = connString // used only to ensure PGMI_TEST_CONN is set
}
