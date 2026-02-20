package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/preprocessor"
	"github.com/vvka-141/pgmi/internal/sourcemap"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

type managementDBConnFunc func(ctx context.Context, connConfig *pgmi.ConnectionConfig, dbName string) (pgmi.DBConnection, func(), error)

// DeploymentService implements the Deployer interface.
// Thread-Safety: NOT safe for concurrent Deploy() calls on the same instance.
// Create separate instances for concurrent deployments.
type DeploymentService struct {
	connectorFactory func(*pgmi.ConnectionConfig) (pgmi.Connector, error)
	approver         pgmi.Approver
	logger           pgmi.Logger
	sessionManager   pgmi.SessionPreparer
	fileScanner      pgmi.FileScanner
	dbManager        pgmi.DatabaseManager
	mgmtConnector    managementDBConnFunc
}

// NewDeploymentService creates a new DeploymentService with all dependencies injected.
//
// Panic vs. Error Boundary Rationale:
//   - Panics on nil dependencies: These are programmer errors that should fail loudly
//     at application startup, not during request handling. Fail-fast at construction
//     time prevents cryptic nil pointer dereferences deep in call stacks.
//   - Returns errors for runtime conditions: Configuration validation, connection failures,
//     and file system errors are recoverable runtime conditions that should be handled
//     by the caller, not panics.
//
// This distinction ensures unrecoverable setup errors are caught immediately while
// allowing graceful error handling for recoverable operational conditions.
func NewDeploymentService(
	connectorFactory func(*pgmi.ConnectionConfig) (pgmi.Connector, error),
	approver pgmi.Approver,
	logger pgmi.Logger,
	sessionManager pgmi.SessionPreparer,
	fileScanner pgmi.FileScanner,
	dbManager pgmi.DatabaseManager,
) *DeploymentService {
	if connectorFactory == nil {
		panic("connectorFactory cannot be nil")
	}
	if approver == nil {
		panic("approver cannot be nil")
	}
	if logger == nil {
		panic("logger cannot be nil")
	}
	if sessionManager == nil {
		panic("sessionManager cannot be nil")
	}
	if fileScanner == nil {
		panic("fileScanner cannot be nil")
	}
	if dbManager == nil {
		panic("dbManager cannot be nil")
	}

	svc := &DeploymentService{
		connectorFactory: connectorFactory,
		approver:         approver,
		logger:           logger,
		sessionManager:   sessionManager,
		fileScanner:      fileScanner,
		dbManager:        dbManager,
	}
	svc.mgmtConnector = svc.defaultMgmtConnector
	return svc
}

func (s *DeploymentService) defaultMgmtConnector(ctx context.Context, connConfig *pgmi.ConnectionConfig, dbName string) (pgmi.DBConnection, func(), error) {
	mgmtConfig := *connConfig
	mgmtConfig.Database = dbName

	connector, err := s.connectorFactory(&mgmtConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create connector: %w", err)
	}

	pool, err := connector.Connect(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to management database: %w", err)
	}

	dbConn := db.NewPoolAdapter(pool)
	cleanup := func() { pool.Close() }
	return dbConn, cleanup, nil
}

// Deploy executes a deployment using the provided configuration.
// This method orchestrates the deployment workflow by calling smaller, focused methods.
func (s *DeploymentService) Deploy(ctx context.Context, config pgmi.DeploymentConfig) error {
	// Validate and parse configuration
	connConfig, err := s.validateAndParseConfig(config)
	if err != nil {
		return err
	}

	// Handle overwrite workflow if requested (drop and recreate database)
	if config.Overwrite {
		if err := s.handleOverwrite(ctx, connConfig, config); err != nil {
			return fmt.Errorf("overwrite workflow failed: %w", err)
		}
	} else {
		// If not overwriting, ensure database exists (create if missing)
		if err := s.ensureDatabaseExists(ctx, connConfig, config); err != nil {
			return fmt.Errorf("failed to ensure database exists: %w", err)
		}
	}

	// Prepare deployment session (scan files, connect to database, load session tables)
	// SessionManager handles: file scanning, database connection, utility functions, files, params
	targetConfig := *connConfig
	targetConfig.Database = config.DatabaseName
	session, err := s.sessionManager.PrepareSession(ctx, &targetConfig, config.SourcePath, config.Parameters, config.Compat, config.Verbose)
	if err != nil {
		return err // Error already wrapped by SessionManager
	}
	defer session.Close()

	if err := s.executeDeploySQL(ctx, session.Conn(), config.SourcePath); err != nil {
		return err
	}

	s.logger.Info("✓ Deployment completed successfully")
	return nil
}

// validateAndParseConfig validates the configuration and parses the connection string.
func (s *DeploymentService) validateAndParseConfig(config pgmi.DeploymentConfig) (*pgmi.ConnectionConfig, error) {
	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	s.logger.Verbose("Starting deployment to database '%s'", config.DatabaseName)
	s.logger.Verbose("Source path: %s", config.SourcePath)

	// Parse connection string
	connConfig, err := s.parseConnectionString(config.ConnectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	// Set application name if not already set
	if connConfig.AppName == "" {
		connConfig.AppName = "pgmi"
	}

	// Apply auth method and cloud credentials from deployment config
	connConfig.AuthMethod = config.AuthMethod
	connConfig.AzureTenantID = config.AzureTenantID
	connConfig.AzureClientID = config.AzureClientID
	connConfig.AzureClientSecret = config.AzureClientSecret

	return connConfig, nil
}

// executeDeploySQL reads, preprocesses, and executes the deploy.sql file.
// Preprocessing expands CALL pgmi_test() macros by querying pgmi_test_plan() from SQL.
func (s *DeploymentService) executeDeploySQL(
	ctx context.Context,
	conn *pgxpool.Conn,
	sourcePath string,
) error {
	s.logger.Verbose("Executing deploy.sql...")

	deploySQL, err := s.fileScanner.ReadDeploySQL(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read deploy.sql: %w", err)
	}

	// Preprocess: expand CALL pgmi_test() macros by querying pgmi_test_plan() from SQL
	pipeline := preprocessor.NewPipeline()
	result, err := pipeline.Process(ctx, conn, deploySQL)
	if err != nil {
		return fmt.Errorf("failed to preprocess deploy.sql: %w", err)
	}

	if result.MacroCount > 0 {
		s.logger.Verbose("Expanded %d test macro(s) in deploy.sql", result.MacroCount)
	}

	// Execute preprocessed deploy.sql as a single script
	_, err = conn.Exec(ctx, result.ExpandedSQL)
	if err != nil {
		// Try to attribute the error to the original source using the source map
		attributedErr := s.attributeError(err, result.SourceMap)
		return fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, attributedErr)
	}

	s.logger.Info("✓ Execution plan built successfully")
	return nil
}

// attributeError attempts to resolve error line numbers back to original sources.
// If the error contains line information and the source map has a mapping,
// returns an enhanced error with the original source context.
func (s *DeploymentService) attributeError(err error, sm *sourcemap.SourceMap) error {
	if sm == nil || sm.Len() == 0 {
		return err
	}

	// Extract PostgreSQL error
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	// PostgreSQL errors may have line info in the message or Position field
	line := extractLineFromError(pgErr)
	if line == 0 {
		return err
	}

	// Try to resolve the line using source map
	file, origLine, desc, found := sm.Resolve(line)
	if !found {
		return err
	}

	// Create enhanced error message
	return fmt.Errorf("%w\n  → %s (line %d: %s)", err, file, origLine, desc)
}

// extractLineFromError extracts a line number from a PostgreSQL error.
// Checks Position field and parses "LINE X:" from the message.
func extractLineFromError(pgErr *pgconn.PgError) int {
	// PostgreSQL doesn't have a Line field in pgconn.PgError for query errors,
	// but the Position field indicates character offset. We can also check
	// the message for "LINE X:" pattern which appears in syntax errors.

	// Check if message contains "LINE X:" pattern
	if idx := strings.Index(pgErr.Message, "LINE "); idx != -1 {
		remaining := pgErr.Message[idx+5:]
		if colonIdx := strings.Index(remaining, ":"); colonIdx != -1 {
			if line, err := strconv.Atoi(remaining[:colonIdx]); err == nil {
				return line
			}
		}
	}

	// For context errors, check Where field
	if pgErr.Where != "" {
		if idx := strings.Index(pgErr.Where, "line "); idx != -1 {
			remaining := pgErr.Where[idx+5:]
			endIdx := strings.IndexAny(remaining, " ,)")
			if endIdx == -1 {
				endIdx = len(remaining)
			}
			if line, err := strconv.Atoi(remaining[:endIdx]); err == nil {
				return line
			}
		}
	}

	return 0
}

func validateOverwriteTarget(targetDB, managementDB string) error {
	if strings.EqualFold(targetDB, managementDB) {
		return fmt.Errorf(
			"cannot overwrite database %q: it is the management database pgmi connects to for server-level operations. "+
				"Deploy to a different target database: %w",
			targetDB, pgmi.ErrInvalidConfig,
		)
	}
	if pgmi.IsTemplateDatabase(targetDB) {
		return fmt.Errorf(
			"cannot overwrite database %q: PostgreSQL template databases cannot be dropped: %w",
			targetDB, pgmi.ErrInvalidConfig,
		)
	}
	return nil
}

// handleOverwrite handles the database drop and recreate workflow.
func (s *DeploymentService) handleOverwrite(ctx context.Context, connConfig *pgmi.ConnectionConfig, config pgmi.DeploymentConfig) error {
	managementDB := config.MaintenanceDatabase
	if managementDB == "" {
		managementDB = pgmi.DefaultManagementDB
	}

	if err := validateOverwriteTarget(config.DatabaseName, managementDB); err != nil {
		return err
	}

	s.logger.Verbose("Connecting to management database '%s'", managementDB)

	dbConn, cleanup, err := s.mgmtConnector(ctx, connConfig, managementDB)
	if err != nil {
		return err
	}
	defer cleanup()

	// Check if target database exists
	exists, err := s.dbManager.Exists(ctx, dbConn, config.DatabaseName)
	if err != nil {
		return fmt.Errorf("failed to check if database exists: %w", err)
	}

	if !exists {
		s.logger.Info("Database '%s' does not exist. Creating...", config.DatabaseName)
		if err := s.dbManager.Create(ctx, dbConn, config.DatabaseName); err != nil {
			return fmt.Errorf("failed to create database: %w", err)
		}
		return nil
	}

	// Request approval for overwrite
	s.logger.Verbose("Database '%s' exists. Requesting approval for overwrite.", config.DatabaseName)
	approved, err := s.approver.RequestApproval(ctx, config.DatabaseName)
	if err != nil {
		return fmt.Errorf("approval request failed: %w", err)
	}

	if !approved {
		return pgmi.ErrApprovalDenied
	}

	// Terminate all connections to target database
	s.logger.Verbose("Terminating all connections to database '%s'", config.DatabaseName)
	if err := s.dbManager.TerminateConnections(ctx, dbConn, config.DatabaseName); err != nil {
		return fmt.Errorf("failed to terminate connections: %w", err)
	}

	// Drop database
	s.logger.Verbose("Dropping database '%s'", config.DatabaseName)
	if err := s.dbManager.Drop(ctx, dbConn, config.DatabaseName); err != nil {
		return fmt.Errorf("failed to drop database: %w", err)
	}

	// Create database
	s.logger.Verbose("Creating database '%s'", config.DatabaseName)
	if err := s.dbManager.Create(ctx, dbConn, config.DatabaseName); err != nil {
		return fmt.Errorf("failed to create database: %w", err)
	}

	s.logger.Info("✓ Database '%s' overwritten successfully", config.DatabaseName)
	return nil
}

// ensureDatabaseExists ensures the target database exists, creating it if necessary.
func (s *DeploymentService) ensureDatabaseExists(ctx context.Context, connConfig *pgmi.ConnectionConfig, config pgmi.DeploymentConfig) error {
	managementDB := config.MaintenanceDatabase
	if managementDB == "" {
		managementDB = pgmi.DefaultManagementDB
	}

	s.logger.Verbose("Connecting to management database '%s' to check if target database exists", managementDB)

	dbConn, cleanup, err := s.mgmtConnector(ctx, connConfig, managementDB)
	if err != nil {
		return err
	}
	defer cleanup()

	exists, err := s.dbManager.Exists(ctx, dbConn, config.DatabaseName)
	if err != nil {
		return fmt.Errorf("failed to check if database exists: %w", err)
	}

	if !exists {
		s.logger.Info("Database '%s' does not exist. Creating...", config.DatabaseName)
		if err := s.dbManager.Create(ctx, dbConn, config.DatabaseName); err != nil {
			return fmt.Errorf("failed to create database: %w", err)
		}
		s.logger.Verbose("✓ Database '%s' created successfully", config.DatabaseName)
	} else {
		s.logger.Verbose("Database '%s' already exists", config.DatabaseName)
	}

	return nil
}

// parseConnectionString parses a connection string using the db package parser.
func (s *DeploymentService) parseConnectionString(connStr string) (*pgmi.ConnectionConfig, error) {
	return db.ParseConnectionString(connStr)
}
