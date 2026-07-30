package testing

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/logging"
	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/internal/testinfra"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var (
	testContainerOnce sync.Once
	testContainerConn string
	testContainerErr  error
)

func getOrStartTestContainer() (string, error) {
	testContainerOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				testContainerErr = fmt.Errorf("testcontainer startup panicked: %v", r)
			}
		}()
		ctx := context.Background()
		container, err := testinfra.StartSimplePostgres(ctx)
		if err != nil {
			testContainerErr = err
			return
		}
		testContainerConn = container.ConnString
	})
	return testContainerConn, testContainerErr
}

// GetTestConnectionString returns the test database connection string.
// Priority: PGMI_TEST_CONN env var > auto-started testcontainer > skip test.
//
// Set PGMI_REQUIRE_DB to turn the skip into a failure. CI does, because a
// registry hiccup or a stopped daemon would otherwise skip every integration
// test and report the job green.
func GetTestConnectionString(t *testing.T) string {
	t.Helper()

	conn, msg, fatal := resolveTestConnection(
		os.Getenv("PGMI_TEST_CONN"),
		os.Getenv("PGMI_REQUIRE_DB"),
		getOrStartTestContainer,
	)
	switch {
	case msg == "":
		return conn
	case fatal:
		t.Fatal(msg)
	default:
		t.Skip(msg)
	}
	return ""
}

// resolveTestConnection decides how to react to each way of obtaining a test
// database. An empty msg means conn is usable; otherwise fatal says whether the
// caller must fail rather than skip. Split out from GetTestConnectionString so
// the requirement can be tested without making the Docker daemon unreachable.
func resolveTestConnection(connEnv, requireEnv string, start func() (string, error)) (conn, msg string, fatal bool) {
	if connEnv != "" {
		return connEnv, "", false
	}

	conn, err := start()
	if err == nil {
		return conn, "", false
	}

	switch strings.ToLower(strings.TrimSpace(requireEnv)) {
	case "", "0", "false", "no":
		return "", fmt.Sprintf("PGMI_TEST_CONN not set and Docker unavailable: %v", err), false
	default:
		return "", fmt.Sprintf("PGMI_REQUIRE_DB is set but no test database is available: %v", err), true
	}
}

// SkipIfShort skips the test if running in short mode (-short flag).
func SkipIfShort(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
}

// RequireDatabase combines SkipIfShort and GetTestConnectionString for convenience.
// Returns the test connection string if available, otherwise skips the test.
func RequireDatabase(t *testing.T) string {
	t.Helper()

	SkipIfShort(t)
	return GetTestConnectionString(t)
}

// NewTestDeployer creates a Deployer instance configured for testing.
// Uses the standard connector factory and a force-approving test approver.
func NewTestDeployer(t *testing.T) pgmi.Deployer {
	t.Helper()

	approver := &ForceApprover{}
	logger := logging.NewNullLogger()
	fileScanner := scanner.NewScanner(checksum.New())
	fileLoader := loader.NewLoader()
	dbManager := manager.New()

	// Create session manager for shared session initialization logic
	sessionManager := services.NewSessionManager(
		db.NewConnector,
		fileScanner,
		fileLoader,
		logger,
	)

	return services.NewDeploymentService(
		db.NewConnector,
		approver,
		logger,
		sessionManager,
		fileScanner,
		dbManager,
	)
}

// NewTestDeployerWithFS creates a Deployer instance configured for testing with a custom filesystem provider.
// This allows testing with embedded filesystems or in-memory filesystems.
func NewTestDeployerWithFS(t *testing.T, fsProvider filesystem.FileSystemProvider) pgmi.Deployer {
	t.Helper()

	approver := &ForceApprover{}
	logger := logging.NewNullLogger()
	fileScanner := scanner.NewScannerWithFS(checksum.New(), fsProvider)
	fileLoader := loader.NewLoader()
	dbManager := manager.New()

	// Create session manager for shared session initialization logic
	sessionManager := services.NewSessionManager(
		db.NewConnector,
		fileScanner,
		fileLoader,
		logger,
	)

	return services.NewDeploymentService(
		db.NewConnector,
		approver,
		logger,
		sessionManager,
		fileScanner,
		dbManager,
	)
}

// ForceApprover is a test approver that always approves overwrite requests.
type ForceApprover struct{}

// RequestApproval always returns true (auto-approves).
func (a *ForceApprover) RequestApproval(ctx context.Context, dbName string) (bool, error) {
	return true, nil
}

// CreateTestDB creates a test database with the given name.
// Returns a cleanup function that should be called with t.Cleanup().
func CreateTestDB(t *testing.T, connString, dbName string) func() {
	t.Helper()

	ctx := context.Background()

	// Connect to management database
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("Failed to connect for test DB creation: %v", err)
	}

	// Create the database
	createQuery := fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{dbName}.Sanitize())
	_, err = pool.Exec(ctx, createQuery)
	if err != nil {
		pool.Close()
		t.Fatalf("Failed to create test database %s: %v", dbName, err)
	}

	pool.Close()
	t.Logf("✓ Created test database %s", dbName)

	// Return cleanup function
	return func() {
		CleanupTestDB(t, connString, dbName)
	}
}

// CleanupTestDB drops the test database.
// Safe to call multiple times (uses DROP DATABASE IF EXISTS).
func CleanupTestDB(t *testing.T, connString, dbName string) {
	t.Helper()

	ctx := context.Background()

	// Connect to management database
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Logf("Warning: Failed to connect for cleanup: %v", err)
		return
	}
	defer pool.Close()

	// Terminate all connections to the database
	terminateQuery := `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE datname = $1 AND pid <> pg_backend_pid()
	`
	_, err = pool.Exec(ctx, terminateQuery, dbName)
	if err != nil {
		t.Logf("Warning: Failed to terminate connections to %s: %v", dbName, err)
	}

	// Drop the database
	dropQuery := fmt.Sprintf("DROP DATABASE IF EXISTS %s", pgx.Identifier{dbName}.Sanitize())
	_, err = pool.Exec(ctx, dropQuery)
	if err != nil {
		t.Logf("Warning: Failed to drop database %s: %v", dbName, err)
	} else {
		t.Logf("✓ Cleaned up database %s", dbName)
	}
}

// GetTestPool creates a connection pool to the specified database for testing.
// The pool is automatically closed when the test completes.
func GetTestPool(t *testing.T, connString, dbName string) *pgxpool.Pool {
	t.Helper()

	ctx := context.Background()

	// Parse connection string
	config, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatalf("Failed to parse connection string: %v", err)
	}

	// Override database name
	config.Database = dbName

	// Build connection string for target database
	targetConnString := db.BuildConnectionString(config)

	// Create pool
	pool, err := pgxpool.New(ctx, targetConnString)
	if err != nil {
		t.Fatalf("Failed to create connection pool: %v", err)
	}

	// Register cleanup
	t.Cleanup(func() {
		pool.Close()
	})

	return pool
}
