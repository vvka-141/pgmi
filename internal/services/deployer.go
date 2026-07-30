package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/contract"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/preprocessor"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// DeployResult contains statistics from a completed deployment.
type DeployResult struct {
	FilesLoaded    int
	TestMacros     int
	Duration       time.Duration
	Database       string
	ExecutionUnits int
	UnitsCommitted int
	ExecutionMode  string
}

type maintenanceDBConnFunc func(ctx context.Context, connConfig *pgmi.ConnectionConfig, dbName string) (pgmi.DBConnection, func(), error)

// DeploymentService implements the Deployer interface.
// Not safe for concurrent Deploy() calls on the same instance.
type DeploymentService struct {
	connectorFactory func(*pgmi.ConnectionConfig) (pgmi.Connector, error)
	approver         pgmi.Approver
	logger           pgmi.Logger
	sessionManager   pgmi.SessionPreparer
	fileScanner      pgmi.FileScanner
	dbManager        pgmi.DatabaseManager
	mgmtConnector    maintenanceDBConnFunc
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
		return nil, nil, fmt.Errorf("failed to connect to maintenance database: %w", err)
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

	// Scan the project before touching the server: a typo'd path, a missing
	// deploy.sql or an unreadable file must not leave a freshly created
	// database behind.
	scanResult, err := s.sessionManager.ScanProject(config.SourcePath)
	if err != nil {
		return fmt.Errorf("file scanning failed: %w", err)
	}

	// --compat is resolved entirely offline, so rejecting it here keeps
	// --overwrite from dropping and recreating a database only to fail on a
	// check that never needed the server.
	if _, _, err := contract.Load(config.Compat); err != nil {
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
	session, err := s.sessionManager.PrepareSession(ctx, &targetConfig, scanResult, config.Parameters, config.Compat, config.Verbose)
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

	// Execute deploy.sql as a sequence of simple-query messages on ONE
	// connection: everything through the first top-level transaction
	// terminator is one message (atomic mode — unchanged semantics), each
	// top-level statement after it is its own message (psql mode:
	// per-statement autocommit, so CREATE INDEX CONCURRENTLY works and an
	// explicit BEGIN ... COMMIT forms a real transaction). A script with no
	// top-level terminator stays a single message. A mid-tail failure leaves
	// earlier autocommitted units applied — that is the documented contract.
	//
	// Attach the exact text sent per unit: PostgreSQL reports error positions
	// as offsets into it, and nothing downstream can resolve them to a line
	// without it. Tail units are whitespace-padded to full-script offsets, so
	// positions and line numbers resolve identically to a single message.
	units := preprocessor.SplitExecutionUnits(result.ExpandedSQL)
	s.lastResult.ExecutionUnits = len(units)
	if len(units) > 1 {
		s.logger.Verbose("deploy.sql splits into %d execution units at the first top-level transaction terminator", len(units))
	}
	for i, unit := range units {
		if _, err := conn.Exec(ctx, unit); err != nil {
			s.lastResult.UnitsCommitted = i
			if i == 0 {
				s.lastResult.ExecutionMode = "atomic"
			} else {
				s.lastResult.ExecutionMode = "psql"
			}
			scriptErr := pgmi.NewScriptError(err, "deploy.sql", unit, result.MacroCount > 0)
			return result.MacroCount, fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, scriptErr)
		}
	}
	s.lastResult.UnitsCommitted = len(units)

	return result.MacroCount, nil
}

func validateOverwriteTarget(targetDB, maintenanceDB string) error {
	if strings.EqualFold(targetDB, maintenanceDB) {
		return fmt.Errorf("cannot overwrite maintenance database %q\npgmi connects to it for CREATE/DROP DATABASE; pick a different target with -d: %w", targetDB, pgmi.ErrInvalidConfig)
	}
	if pgmi.IsTemplateDatabase(targetDB) {
		return fmt.Errorf("cannot drop template database %q (template0/template1 are protected by PostgreSQL): %w", targetDB, pgmi.ErrInvalidConfig)
	}
	return nil
}

// connectMaintenance resolves the maintenance DB name (defaulting to "postgres") and connects.
func (s *DeploymentService) connectMaintenance(ctx context.Context, connConfig *pgmi.ConnectionConfig, maintenanceDB string) (pgmi.DBConnection, func(), error) {
	mgmtDB := maintenanceDB
	if mgmtDB == "" {
		mgmtDB = pgmi.DefaultMaintenanceDB
	}
	s.logger.Verbose("Connecting to maintenance database %q", mgmtDB)
	return s.mgmtConnector(ctx, connConfig, mgmtDB)
}

// createIfMissing checks whether the database exists and creates it if not.
// Returns true if the database already existed.
func (s *DeploymentService) createIfMissing(ctx context.Context, dbConn pgmi.DBConnection, dbName string) (existed bool, err error) {
	exists, err := s.dbManager.Exists(ctx, dbConn, dbName)
	if err != nil {
		return false, err // manager.Exists already names the operation and database
	}
	if !exists {
		s.logger.Info("Database %q does not exist; creating", dbName)
		// Nil settings: a database that does not exist yet has nothing to
		// preserve, so it takes the server defaults as PostgreSQL intends.
		if err := s.dbManager.Create(ctx, dbConn, dbName, nil); err != nil {
			return false, classifyCreateFailure(err)
		}
		return false, nil
	}
	return true, nil
}

// handleOverwrite handles the database drop and recreate workflow.
func (s *DeploymentService) handleOverwrite(ctx context.Context, connConfig *pgmi.ConnectionConfig, config pgmi.DeploymentConfig) error {
	managementDB := config.MaintenanceDatabase
	if managementDB == "" {
		managementDB = pgmi.DefaultMaintenanceDB
	}

	if err := validateOverwriteTarget(config.DatabaseName, managementDB); err != nil {
		return err
	}

	dbConn, cleanup, err := s.connectMaintenance(ctx, connConfig, managementDB)
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

	// Read the encoding and locale before the database ceases to exist. Without
	// this, --overwrite recreated it from the server defaults, so a LATIN1/C
	// database came back UTF8/en_US.utf8 — a change to how bytes are stored and
	// how every index orders, from a flag that only promises to empty it.
	settings, err := s.dbManager.Settings(ctx, dbConn, config.DatabaseName)
	if err != nil {
		return err
	}
	if settings != nil {
		s.logger.Verbose("Preserving encoding %s, collation %s across the recreate",
			settings.Encoding, settings.Collate)
	}

	s.logger.Verbose("Terminating connections to %q", config.DatabaseName)
	if err := s.dbManager.TerminateConnections(ctx, dbConn, config.DatabaseName); err != nil {
		return err // manager.TerminateConnections already names the database
	}

	s.logger.Verbose("DROP DATABASE %q", config.DatabaseName)
	if err := s.dbManager.Drop(ctx, dbConn, config.DatabaseName); err != nil {
		return err // manager.Drop already names the database
	}

	s.logger.Verbose("CREATE DATABASE %q", config.DatabaseName)
	if err := s.dbManager.Create(ctx, dbConn, config.DatabaseName, settings); err != nil {
		return classifyCreateFailure(err)
	}

	s.logger.Info("Recreated database %q", config.DatabaseName)
	return nil
}

// ensureDatabaseExists ensures the target database exists, creating it if necessary.
func (s *DeploymentService) ensureDatabaseExists(ctx context.Context, connConfig *pgmi.ConnectionConfig, config pgmi.DeploymentConfig) error {
	// Probe the target directly. When it answers, the database exists and no
	// maintenance connection is needed at all -- which is what lets a CI role
	// holding CONNECT on only its own database deploy. The maintenance database
	// is dialed solely to create a target that is genuinely absent.
	if _, cleanup, err := s.mgmtConnector(ctx, connConfig, config.DatabaseName); err == nil {
		cleanup()
		s.logger.Verbose("Database %q already exists", config.DatabaseName)
		return nil
	} else if !isUndefinedDatabase(err) {
		return err
	}

	s.logger.Verbose("Database %q not found; using the maintenance database to create it", config.DatabaseName)

	dbConn, cleanup, err := s.connectMaintenance(ctx, connConfig, config.MaintenanceDatabase)
	if err != nil {
		return err
	}
	defer cleanup()

	if _, err := s.createIfMissing(ctx, dbConn, config.DatabaseName); err != nil {
		return err
	}
	s.logger.Verbose("Created database %q", config.DatabaseName)

	return nil
}

// isUndefinedDatabase reports whether err is PostgreSQL's invalid_catalog_name,
// the response to connecting to a database that does not exist.
func isUndefinedDatabase(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "3D000"
}

// classifyCreateFailure turns a lost CREATE DATABASE race into the concurrent
// deploy result it is.
//
// Create only runs after Exists reported false, so "already exists" coming back
// means another deploy created it in between. Two pgmi runs starting together
// with --overwrite hit this reliably: the advisory lock that serialises them
// lives inside the session, and DROP/CREATE happens before any session exists.
//
// PostgreSQL reports it two ways. 42P04 is the ordinary duplicate_database;
// 23505 on pg_database_datname_index is what a genuine race produces, and it
// reached the user verbatim — an internal catalog index name in place of a
// diagnosis, exiting 1, so CI could not tell a retryable collision from a real
// failure. Both now map to ExitConcurrentDeploy (15).
//
// The wrapper is dropped rather than replaced: manager.Create already says
// "failed to create database %q", so adding to it produced "failed to create
// database: failed to create database ...".
func classifyCreateFailure(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "42P04" || pgErr.Code == "23505") {
		return fmt.Errorf("%w: the database was created by another deploy while this one was starting",
			pgmi.ErrConcurrentDeploy)
	}
	return err
}
