package services

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/contract"
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
// Panics on nil dependencies (programmer error).
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
	compat string,
	verbose bool,
) (*pgmi.Session, error) {
	// Scan and validate source files
	scanResult, err := sm.scanAndValidateFiles(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("file scanning failed: %w", err)
	}

	// Connect to target database
	pool, connectorCleanup, err := sm.connectToDatabase(ctx, connConfig)
	if err != nil {
		return nil, fmt.Errorf("database connection failed: %w", err)
	}

	// Acquire a single connection for the entire session
	// This is critical because pg_temp tables are session-scoped
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		connectorCleanup()
		return nil, fmt.Errorf("failed to acquire connection: %w", err)
	}

	var success bool
	defer func() {
		if !success {
			conn.Release()
			pool.Close()
			connectorCleanup()
		}
	}()

	// Serialise concurrent `pgmi deploy` against the same target database.
	// pg_try_advisory_lock returns false immediately if another session
	// already holds the lock — we surface that as a distinct sentinel so
	// the caller gets a clear message and a dedicated exit code (15)
	// instead of a cryptic mid-deploy SQL error. The lock is session-
	// scoped; releasing the pg_temp session on disconnect releases it.
	//
	// The key is derived from the DB name via hashtextextended (PostgreSQL 11+,
	// pgmi's minimum supported version) so two deployments against DIFFERENT
	// databases on the same cluster do not block each other.
	var lockAcquired bool
	if err := conn.QueryRow(
		ctx,
		`SELECT pg_try_advisory_lock(hashtextextended('pgmi.deploy.' || current_database(), 0))`,
	).Scan(&lockAcquired); err != nil {
		return nil, fmt.Errorf("failed to check deployment advisory lock: %w", err)
	}
	if !lockAcquired {
		return nil, fmt.Errorf("%w: another pgmi deployment is already running against %q",
			pgmi.ErrConcurrentDeploy, connConfig.Database)
	}

	if verbose {
		if _, err := conn.Exec(ctx, "SET client_min_messages = 'debug'"); err != nil {
			return nil, fmt.Errorf("failed to set client_min_messages: %w", err)
		}
	}

	// Prepare session (utility functions, files, params, API contract)
	if err := sm.prepareSessionTables(ctx, conn, &scanResult, parameters, compat); err != nil {
		return nil, fmt.Errorf("session preparation failed: %w", err)
	}

	// Create Session object to encapsulate resources
	session := pgmi.NewSession(pool, conn, connectorCleanup)
	success = true
	return session, nil
}

// scanAndValidateFiles scans the source directory and validates files.
func (sm *SessionManager) scanAndValidateFiles(sourcePath string) (pgmi.FileScanResult, error) {
	sm.logger.Verbose("Scanning %s", sourcePath)

	// Validate deploy.sql exists
	if err := sm.fileScanner.ValidateDeploySQL(sourcePath); err != nil {
		return pgmi.FileScanResult{}, fmt.Errorf("failed to validate deploy.sql: %w", err)
	}

	// Scan all files (excluding deploy.sql)
	scanResult, err := sm.fileScanner.ScanDirectory(sourcePath)
	if err != nil {
		return pgmi.FileScanResult{}, fmt.Errorf("failed to scan directory %q: %w", sourcePath, err)
	}

	if err := validateNoDuplicateScriptIDs(scanResult.Files); err != nil {
		return pgmi.FileScanResult{}, err
	}

	sm.logger.Verbose("Found %d files to load", len(scanResult.Files))

	return scanResult, nil
}

// validateNoDuplicateScriptIDs fails fast when two files share a <pgmi-meta id>.
// A shared id makes the second one-time script silently skip on deploy (its id
// already has an execution-log row), so this is rejected before any connection
// or SQL execution. Returns an ErrInvalidConfig (exit code 10) naming the id and
// every conflicting file.
func validateNoDuplicateScriptIDs(files []pgmi.FileMetadata) error {
	pathsByID := make(map[uuid.UUID][]string)
	for _, f := range files {
		if f.Metadata == nil || f.Metadata.ID == uuid.Nil {
			continue
		}
		pathsByID[f.Metadata.ID] = append(pathsByID[f.Metadata.ID], f.Path)
	}

	var conflicts []string
	for id, paths := range pathsByID {
		if len(paths) > 1 {
			sort.Strings(paths)
			conflicts = append(conflicts, fmt.Sprintf("  %s: %s", id, strings.Join(paths, ", ")))
		}
	}

	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return fmt.Errorf("duplicate <pgmi-meta id> across files; each script must have a unique id:\n%s: %w",
			strings.Join(conflicts, "\n"), pgmi.ErrInvalidConfig)
	}

	return nil
}

// connectToDatabase establishes a connection to the target database.
// Returns a cleanup function that releases connector-level resources (e.g., Cloud SQL dialer).
// The cleanup function must be called after the pool is closed.
func (sm *SessionManager) connectToDatabase(
	ctx context.Context,
	connConfig *pgmi.ConnectionConfig,
) (*pgxpool.Pool, func(), error) {
	sm.logger.Verbose("Connecting to database %q", connConfig.Database)

	connector, err := sm.connectorFactory(connConfig)
	if err != nil {
		return nil, func() {}, fmt.Errorf("failed to create connector: %w", err)
	}

	pool, err := connector.Connect(ctx)
	if err != nil {
		closeConnector(connector)
		return nil, func() {}, fmt.Errorf("failed to connect to database %q: %w", connConfig.Database, err)
	}

	return pool, func() { closeConnector(connector) }, nil
}

// closeConnector closes connectors that implement io.Closer (e.g., GoogleCloudSQLConnector).
func closeConnector(c pgmi.Connector) {
	if closer, ok := c.(io.Closer); ok {
		closer.Close()
	}
}

// prepareSessionTables prepares the deployment session by creating utility functions
// and loading files and parameters into session-scoped tables.
func (sm *SessionManager) prepareSessionTables(
	ctx context.Context,
	conn *pgxpool.Conn,
	scanResult *pgmi.FileScanResult,
	parameters map[string]string,
	compat string,
) error {
	sm.logger.Verbose("Creating pg_temp internal tables")
	if err := params.CreateSchema(ctx, conn); err != nil {
		return fmt.Errorf("failed to create internal tables: %w", err)
	}

	sm.logger.Verbose("Loading files into pg_temp._pgmi_source")
	if err := sm.fileLoader.LoadFilesIntoSession(ctx, conn, scanResult.Files); err != nil {
		return fmt.Errorf("failed to load files: %w", err)
	}
	sm.logger.Info("Loaded %d files", len(scanResult.Files))

	sm.logger.Verbose("Loading parameters into pg_temp._pgmi_parameter")
	if err := sm.fileLoader.LoadParametersIntoSession(ctx, conn, parameters); err != nil {
		return fmt.Errorf("failed to load parameters: %w", err)
	}
	if len(parameters) > 0 {
		sm.logger.Info("Loaded %d parameters", len(parameters))
	}

	sm.logger.Verbose("Applying API contract")
	appliedVersion, err := contract.Apply(ctx, conn, compat)
	if err != nil {
		return fmt.Errorf("failed to apply API contract: %w", err)
	}
	sm.logger.Verbose("Applied API contract v%s", appliedVersion)

	return nil
}

