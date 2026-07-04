package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/preprocessor"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// DeployResult contains statistics from a completed deployment.
type DeployResult struct {
	FilesLoaded int
	TestMacros  int
	Duration    time.Duration
	Database    string
}

type managementDBConnFunc func(ctx context.Context, connConfig *pgmi.ConnectionConfig, dbName string) (pgmi.DBConnection, func(), error)

// DeploymentService implements the Deployer interface.
// Not safe for concurrent Deploy() calls on the same instance.
type DeploymentService struct {
	connectorFactory func(*pgmi.ConnectionConfig) (pgmi.Connector, error)
	approver         pgmi.Approver
	logger           pgmi.Logger
	sessionManager   pgmi.SessionPreparer
	fileScanner      pgmi.FileScanner
	dbManager        pgmi.DatabaseManager
	mgmtConnector    managementDBConnFunc
	lastResult       *DeployResult
}

// LastResult returns statistics from the most recent Deploy call,
// or nil if Deploy has not been called.
func (s *DeploymentService) LastResult() *DeployResult {
	return s.lastResult
}

var _ pgmi.Deployer = (*DeploymentService)(nil)

// NewDeploymentService creates a new DeploymentService with all dependencies injected.
// Panics on nil dependencies (programmer error); returns errors for runtime conditions.
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
	mgmtConfig := connConfig.DeepCopy()
	mgmtConfig.Database = dbName

	connector, err := s.connectorFactory(&mgmtConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create connector: %w", err)
	}

	pool, err := connector.Connect(ctx)
	if err != nil {
		closeConnector(connector)
		return nil, nil, fmt.Errorf("failed to connect to management database: %w", err)
	}

	dbConn := db.NewPoolAdapter(pool)
	cleanup := func() {
		pool.Close()
		closeConnector(connector)
	}
	return dbConn, cleanup, nil
}

// Deploy executes a deployment using the provided configuration.
// This method orchestrates the deployment workflow by calling smaller, focused methods.
// After Deploy returns, call LastResult() for deployment statistics.
func (s *DeploymentService) Deploy(ctx context.Context, config pgmi.DeploymentConfig) error {
	start := time.Now()
	s.lastResult = &DeployResult{Database: config.DatabaseName}
	defer func() { s.lastResult.Duration = time.Since(start) }()

	// Validate and parse configuration
	connConfig, err := s.validateAndParseConfig(config)
	if err != nil {
		return err
	}

	// Validate the project before touching the server: a typo'd path must not
	// leave a freshly created database behind
	if err := s.fileScanner.ValidateDeploySQL(config.SourcePath); err != nil {
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
	targetConfig := connConfig.DeepCopy()
	targetConfig.Database = config.DatabaseName
	s.logger.Info("Preparing session: scanning files, loading parameters")
	session, err := s.sessionManager.PrepareSession(ctx, &targetConfig, config.SourcePath, config.Parameters, config.Compat, config.Verbose)
	if err != nil {
		return err // Error already wrapped by SessionManager
	}
	defer session.Close()

	s.lastResult.FilesLoaded = session.FilesLoaded

	s.logger.Info("Executing deploy.sql")
	macroCount, err := s.executeDeploySQL(ctx, session.Conn(), config.SourcePath)
	s.lastResult.TestMacros = macroCount
	return err
}

// validateAndParseConfig validates the configuration and parses the connection string.
func (s *DeploymentService) validateAndParseConfig(config pgmi.DeploymentConfig) (*pgmi.ConnectionConfig, error) {
	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, err
	}

	s.logger.Verbose("Deploying to database %q", config.DatabaseName)
	s.logger.Verbose("Source path: %s", config.SourcePath)

	// Parse connection string
	connConfig, err := db.ParseConnectionString(config.ConnectionString)
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
// Returns the number of test macros expanded and any error.
func (s *DeploymentService) executeDeploySQL(
	ctx context.Context,
	conn *pgxpool.Conn,
	sourcePath string,
) (int, error) {
	s.logger.Verbose("Reading deploy.sql")

	deploySQL, err := s.fileScanner.ReadDeploySQL(sourcePath)
	if err != nil {
		return 0, fmt.Errorf("failed to read deploy.sql: %w", err)
	}

	// Preprocess: expand CALL pgmi_test() macros by querying pgmi_test_plan() from SQL
	pipeline := preprocessor.NewPipeline()
	result, err := pipeline.Process(ctx, conn, deploySQL)
	if err != nil {
		return 0, fmt.Errorf("failed to preprocess deploy.sql: %w", err)
	}

	if result.MacroCount > 0 {
		s.logger.Verbose("Expanded %d test macro(s) in deploy.sql", result.MacroCount)
	}

	// Execute preprocessed deploy.sql as a single script
	_, err = conn.Exec(ctx, result.ExpandedSQL)
	if err != nil {
		return result.MacroCount, fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, err)
	}

	return result.MacroCount, nil
}

func validateOverwriteTarget(targetDB, managementDB string) error {
	if strings.EqualFold(targetDB, managementDB) {
		return fmt.Errorf("cannot overwrite management database %q\npgmi connects to it for CREATE/DROP DATABASE; pick a different target with -d: %w", targetDB, pgmi.ErrInvalidConfig)
	}
	if pgmi.IsTemplateDatabase(targetDB) {
		return fmt.Errorf("cannot drop template database %q (template0/template1 are protected by PostgreSQL): %w", targetDB, pgmi.ErrInvalidConfig)
	}
	return nil
}

// connectManagement resolves the management DB name (defaulting to "postgres") and connects.
func (s *DeploymentService) connectManagement(ctx context.Context, connConfig *pgmi.ConnectionConfig, maintenanceDB string) (pgmi.DBConnection, func(), error) {
	mgmtDB := maintenanceDB
	if mgmtDB == "" {
		mgmtDB = pgmi.DefaultManagementDB
	}
	s.logger.Verbose("Connecting to management database %q", mgmtDB)
	return s.mgmtConnector(ctx, connConfig, mgmtDB)
}

// createIfMissing checks whether the database exists and creates it if not.
// Returns true if the database already existed.
func (s *DeploymentService) createIfMissing(ctx context.Context, dbConn pgmi.DBConnection, dbName string) (existed bool, err error) {
	exists, err := s.dbManager.Exists(ctx, dbConn, dbName)
	if err != nil {
		return false, fmt.Errorf("failed to check if database exists: %w", err)
	}
	if !exists {
		s.logger.Info("Database %q does not exist; creating", dbName)
		if err := s.dbManager.Create(ctx, dbConn, dbName); err != nil {
			return false, fmt.Errorf("failed to create database: %w", err)
		}
		return false, nil
	}
	return true, nil
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

	dbConn, cleanup, err := s.connectManagement(ctx, connConfig, managementDB)
	if err != nil {
		return err
	}
	defer cleanup()

	existed, err := s.createIfMissing(ctx, dbConn, config.DatabaseName)
	if err != nil {
		return err
	}
	if !existed {
		return nil
	}

	s.logger.Verbose("Database %q exists; requesting approval", config.DatabaseName)
	approved, err := s.approver.RequestApproval(ctx, config.DatabaseName)
	if err != nil {
		return fmt.Errorf("approval request failed: %w", err)
	}
	if !approved {
		return pgmi.ErrApprovalDenied
	}

	s.logger.Verbose("Terminating connections to %q", config.DatabaseName)
	if err := s.dbManager.TerminateConnections(ctx, dbConn, config.DatabaseName); err != nil {
		return fmt.Errorf("failed to terminate connections: %w", err)
	}

	s.logger.Verbose("DROP DATABASE %q", config.DatabaseName)
	if err := s.dbManager.Drop(ctx, dbConn, config.DatabaseName); err != nil {
		return fmt.Errorf("failed to drop database: %w", err)
	}

	s.logger.Verbose("CREATE DATABASE %q", config.DatabaseName)
	if err := s.dbManager.Create(ctx, dbConn, config.DatabaseName); err != nil {
		return fmt.Errorf("failed to create database: %w", err)
	}

	s.logger.Info("Recreated database %q", config.DatabaseName)
	return nil
}

// ensureDatabaseExists ensures the target database exists, creating it if necessary.
func (s *DeploymentService) ensureDatabaseExists(ctx context.Context, connConfig *pgmi.ConnectionConfig, config pgmi.DeploymentConfig) error {
	dbConn, cleanup, err := s.connectManagement(ctx, connConfig, config.MaintenanceDatabase)
	if err != nil {
		return err
	}
	defer cleanup()

	existed, err := s.createIfMissing(ctx, dbConn, config.DatabaseName)
	if err != nil {
		return err
	}
	if existed {
		s.logger.Verbose("Database %q already exists", config.DatabaseName)
	} else {
		s.logger.Verbose("Created database %q", config.DatabaseName)
	}

	return nil
}
