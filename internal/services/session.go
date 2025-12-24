package services

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// SessionManager handles session initialization shared between deployment and testing.
// Responsibility: Scan files, connect to database, prepare session (utility functions, files, parameters).
//
// SessionManager is thread-safe for concurrent use as long as the injected dependencies
// (connectorFactory, fileScanner, fileLoader, logger) are also thread-safe.
type SessionManager struct {
	connectorFactory func(*pgmi.ConnectionConfig) (pgmi.Connector, error)
	fileScanner      pgmi.FileScanner
	fileLoader       pgmi.FileLoader
	logger           pgmi.Logger
}

// NewSessionManager creates a new SessionManager with all dependencies injected.
//
// Panics if any dependency is nil. This is intentional fail-fast behavior
// to prevent cryptic nil pointer dereferences later. Panics indicate
// programmer error (incorrect dependency injection setup).
func NewSessionManager(
	connectorFactory func(*pgmi.ConnectionConfig) (pgmi.Connector, error),
	fileScanner pgmi.FileScanner,
	fileLoader pgmi.FileLoader,
	logger pgmi.Logger,
) *SessionManager {
	if connectorFactory == nil {
		panic("connectorFactory cannot be nil")
	}
	if fileScanner == nil {
		panic("fileScanner cannot be nil")
	}
	if fileLoader == nil {
		panic("fileLoader cannot be nil")
	}
	if logger == nil {
		panic("logger cannot be nil")
	}

	return &SessionManager{
		connectorFactory: connectorFactory,
		fileScanner:      fileScanner,
		fileLoader:       fileLoader,
		logger:           logger,
	}
}

// PrepareSession scans files, validates, connects to database, and initializes the deployment session.
//
// Returns:
//   - Session object encapsulating pool, connection, and scan results
//   - Error if any step fails
//
// The caller is responsible for:
//   - Closing the session: defer session.Close()
//
// The Session object provides access to Pool(), Conn(), and ScanResult()
// and manages cleanup of all resources through a single Close() method.
func (sm *SessionManager) PrepareSession(
	ctx context.Context,
	connConfig *pgmi.ConnectionConfig,
	sourcePath string,
	parameters map[string]string,
) (*pgmi.Session, error) {
	// Scan and validate source files
	scanResult, err := sm.scanAndValidateFiles(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("file scanning failed: %w", err)
	}

	// Connect to target database
	pool, err := sm.connectToDatabase(ctx, connConfig)
	if err != nil {
		return nil, fmt.Errorf("database connection failed: %w", err)
	}

	// Acquire a single connection for the entire session
	// This is critical because pg_temp tables are session-scoped
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to acquire connection: %w", err)
	}

	// Prepare session (utility functions, files, params, unittest framework)
	if err := sm.prepareSessionTables(ctx, conn, &scanResult, parameters); err != nil {
		conn.Release()
		pool.Close()
		return nil, fmt.Errorf("session preparation failed: %w", err)
	}

	// Create Session object to encapsulate resources
	session := pgmi.NewSession(pool, conn, scanResult)
	return session, nil
}

// scanAndValidateFiles scans the source directory and validates files.
func (sm *SessionManager) scanAndValidateFiles(sourcePath string) (pgmi.FileScanResult, error) {
	sm.logger.Verbose("Scanning files from source directory...")

	// Validate deploy.sql exists
	if err := sm.fileScanner.ValidateDeploySQL(sourcePath); err != nil {
		return pgmi.FileScanResult{}, fmt.Errorf("failed to validate deploy.sql: %w", err)
	}

	// Scan all files (excluding deploy.sql)
	scanResult, err := sm.fileScanner.ScanDirectory(sourcePath)
	if err != nil {
		return pgmi.FileScanResult{}, fmt.Errorf("failed to scan directory %q: %w", sourcePath, err)
	}

	sm.logger.Verbose("Found %d files to load", len(scanResult.Files))

	return scanResult, nil
}

// connectToDatabase establishes a connection to the target database.
func (sm *SessionManager) connectToDatabase(
	ctx context.Context,
	connConfig *pgmi.ConnectionConfig,
) (*pgxpool.Pool, error) {
	sm.logger.Verbose("Connecting to database '%s'", connConfig.Database)

	connector, err := sm.connectorFactory(connConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connector: %w", err)
	}

	pool, err := connector.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database %q: %w", connConfig.Database, err)
	}

	return pool, nil
}

// prepareSessionTables prepares the deployment session by creating utility functions
// and loading files and parameters into session-scoped tables.
func (sm *SessionManager) prepareSessionTables(
	ctx context.Context,
	conn *pgxpool.Conn,
	scanResult *pgmi.FileScanResult,
	parameters map[string]string,
) error {
	// Create utility functions in pg_temp schema
	sm.logger.Verbose("Creating utility functions in pg_temp schema...")
	if err := params.CreateSchema(ctx, conn); err != nil {
		return fmt.Errorf("failed to create utility functions: %w", err)
	}
	sm.logger.Info("✓ Created utility functions in pg_temp schema")

	// Load files into pg_temp.pgmi_source table
	sm.logger.Verbose("Loading files into pg_temp.pgmi_source table...")
	if err := sm.fileLoader.LoadFilesIntoSession(ctx, conn, scanResult.Files); err != nil {
		return fmt.Errorf("failed to load files: %w", err)
	}
	sm.logger.Info("✓ Loaded %d files into pg_temp.pgmi_source", len(scanResult.Files))

	// Load parameters into pg_temp.pgmi_parameter table
	sm.logger.Verbose("Loading parameters into pg_temp.pgmi_parameter table...")
	if err := sm.fileLoader.LoadParametersIntoSession(ctx, conn, parameters); err != nil {
		return fmt.Errorf("failed to load parameters: %w", err)
	}
	sm.logger.Info("✓ Loaded %d parameters into pg_temp.pgmi_parameter", len(parameters))

	// Create unittest framework in pg_temp schema (after pgmi_source is populated)
	// This executes unittest.sql which moves test files from pgmi_source and materializes the execution plan
	sm.logger.Verbose("Creating unit test framework in pg_temp schema...")
	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		return fmt.Errorf("failed to create unittest framework: %w", err)
	}
	sm.logger.Info("✓ Created unit test framework in pg_temp schema")

	return nil
}

