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
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/internal/testhelpers/fixtures"
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

	sm := services.NewSessionManager(db.ConnectorFactory(db.Output{}), fileScanner, fileLoader, logger, "test")

	session, err := sm.PrepareSession(ctx, connConfig, mustScanProject(t, sm), map[string]string{"env": "test"}, "", false)
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
	if err := session.Conn().QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_source_view").Scan(&count); err != nil {
		t.Fatalf("Failed to query pgmi_source_view: %v", err)
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

	sm := services.NewSessionManager(db.ConnectorFactory(db.Output{}), fileScanner, fileLoader, logger, "test")

	session, err := sm.PrepareSession(ctx, connConfig, mustScanProject(t, sm), nil, "", true)
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

func TestPrepareSession_VerboseSetupIsQuiet(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_session_verbose_quiet"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	connConfig, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatalf("Failed to parse connection string: %v", err)
	}
	connConfig.Database = testDB

	var debugLines []string
	notice := func(severity, message, _, _ string) {
		if severity == "DEBUG" {
			debugLines = append(debugLines, message)
		}
	}

	sm := services.NewSessionManager(db.ConnectorFactory(db.Output{Notice: notice}), scanner.NewScannerWithFS(checksum.New(), fixtures.EmptyProject()),
		loader.NewLoader(), logging.NewNullLogger(), "test")
	session, err := sm.PrepareSession(ctx, connConfig, mustScanProject(t, sm), nil, "", true)
	if err != nil {
		t.Fatalf("PrepareSession with verbose failed: %v", err)
	}
	defer session.Close()

	if len(debugLines) != 0 {
		t.Errorf("pgmi's own session setup sent %d server DEBUG lines under --verbose, first: %q", len(debugLines), debugLines[0])
	}
}

func TestPrepareSession_VerboseForwardsOnlyRaiseDebug(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_session_verbose_raise_debug"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	connConfig, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatalf("Failed to parse connection string: %v", err)
	}
	connConfig.Database = testDB

	var debugLines []string
	notice := func(severity, message, _, _ string) {
		if severity == "DEBUG" {
			debugLines = append(debugLines, message)
		}
	}

	sm := services.NewSessionManager(db.ConnectorFactory(db.Output{Notice: notice}), scanner.NewScannerWithFS(checksum.New(), fixtures.EmptyProject()),
		loader.NewLoader(), logging.NewNullLogger(), "test")
	session, err := sm.PrepareSession(ctx, connConfig, mustScanProject(t, sm), nil, "", true)
	if err != nil {
		t.Fatalf("PrepareSession with verbose failed: %v", err)
	}
	defer session.Close()

	if _, err := session.Conn().Exec(ctx, "DO $$ BEGIN RAISE DEBUG 'mine'; END $$; CREATE TABLE t (id int PRIMARY KEY)"); err != nil {
		t.Fatalf("exec: %v", err)
	}

	if len(debugLines) != 1 || debugLines[0] != "mine" {
		t.Errorf("DEBUG lines under --verbose = %q, want only the RAISE DEBUG line \"mine\"", debugLines)
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

	sm := services.NewSessionManager(db.ConnectorFactory(db.Output{}), fileScanner, fileLoader, logger, "test")

	params := map[string]string{"env": "staging", "version": "3.0"}
	session, err := sm.PrepareSession(ctx, connConfig, mustScanProject(t, sm), params, "", false)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}
	defer session.Close()

	var fileCount int
	if err := session.Conn().QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_source_view").Scan(&fileCount); err != nil {
		t.Fatalf("Failed to count files: %v", err)
	}
	if fileCount != 2 {
		t.Errorf("Expected 2 files, got %d", fileCount)
	}

	var paramCount int
	if err := session.Conn().QueryRow(ctx, "SELECT count(*) FROM pg_temp.pgmi_parameter_view").Scan(&paramCount); err != nil {
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

	connConfig := &db.ConnectionConfig{
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

	sm := services.NewSessionManager(db.ConnectorFactory(db.Output{}), fileScanner, fileLoader, logger, "test")

	_, err := sm.PrepareSession(context.Background(), connConfig, mustScanProject(t, sm), nil, "", false)
	if err == nil {
		t.Fatal("Expected error for invalid connection")
	}

	_ = connString // used only to ensure PGMI_TEST_CONN is set
}

// mustScanProject scans the fixture project or fails the test; PrepareSession
// takes the scan result so callers can validate before creating a database.
func mustScanProject(t *testing.T, sm *services.SessionManager) scanner.Result {
	t.Helper()
	result, err := sm.ScanProject("/")
	if err != nil {
		t.Fatalf("ScanProject failed: %v", err)
	}
	return result
}
