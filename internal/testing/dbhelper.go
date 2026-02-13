package testing

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

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
func GetTestConnectionString(t *testing.T) string {
	t.Helper()

	if connString := os.Getenv("PGMI_TEST_CONN"); connString != "" {
		return connString
	}

	connString, err := getOrStartTestContainer()
	if err != nil {
		t.Skipf("PGMI_TEST_CONN not set and Docker unavailable: %v", err)
	}
	return connString
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
	createQuery := fmt.Sprintf("CREATE DATABASE %s", dbName)
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
	dropQuery := fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName)
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
