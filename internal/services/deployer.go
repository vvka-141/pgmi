package services

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// DeploymentService implements the Deployer interface.
// Thread-Safety: NOT safe for concurrent Deploy() calls on the same instance.
// Create separate instances for concurrent deployments.
type DeploymentService struct {
	connectorFactory func(*pgmi.ConnectionConfig) (pgmi.Connector, error)
	approver         pgmi.Approver
	logger           pgmi.Logger
	sessionManager   *SessionManager
	fileScanner      pgmi.FileScanner
	dbManager        pgmi.DatabaseManager
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
	sessionManager *SessionManager,
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

	return &DeploymentService{
		connectorFactory: connectorFactory,
		approver:         approver,
		logger:           logger,
		sessionManager:   sessionManager,
		fileScanner:      fileScanner,
		dbManager:        dbManager,
	}
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
	session, err := s.sessionManager.PrepareSession(ctx, &targetConfig, config.SourcePath, config.Parameters, config.Verbose)
	if err != nil {
		return err // Error already wrapped by SessionManager
	}
	defer session.Close()

	// Execute deploy.sql (populates pg_temp.pgmi_plan table)
	if err := s.executeDeploySQL(ctx, session.Conn(), config.SourcePath); err != nil {
		return fmt.Errorf("deploy.sql execution failed: %w", err)
	}

	// Execute commands from pg_temp.pgmi_plan table
	if err := s.executePlannedCommands(ctx, session.Conn()); err != nil {
		return fmt.Errorf("command execution failed: %w", err)
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

// executeDeploySQL reads, validates, and executes the deploy.sql file.
// deploy.sql populates the pg_temp.pgmi_plan table with commands to be executed.
func (s *DeploymentService) executeDeploySQL(
	ctx context.Context,
	conn *pgxpool.Conn,
	sourcePath string,
) error {
	s.logger.Verbose("Executing deploy.sql to build execution plan...")

	deploySQL, err := s.fileScanner.ReadDeploySQL(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read deploy.sql: %w", err)
	}

	// Validate deploy.sql follows required workflow
	// Execute deploy.sql as a single script
	_, err = conn.Exec(ctx, deploySQL)
	if err != nil {
		return fmt.Errorf("deploy.sql execution failed: %w", errors.Join(pgmi.ErrExecutionFailed, err))
	}

	s.logger.Info("✓ Execution plan built successfully")
	return nil
}

// executePlannedCommands reads and executes all commands from pg_temp.pgmi_plan table.
func (s *DeploymentService) executePlannedCommands(ctx context.Context, conn *pgxpool.Conn) error {
	s.logger.Verbose("Executing planned commands from pg_temp.pgmi_plan...")

	// Query all commands ordered by ordinal
	rows, err := conn.Query(ctx, queryPlanCommands)
	if err != nil {
		return fmt.Errorf("failed to query pg_temp.pgmi_plan: %w", err)
	}

	// Read all commands into memory first, then close the query
	type command struct {
		ordinal int
		sql     string
	}
	var commands []command

	for rows.Next() {
		var cmd command
		if err := rows.Scan(&cmd.ordinal, &cmd.sql); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan command row: %w", err)
		}
		commands = append(commands, cmd)
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error reading command rows: %w", err)
	}

	// Now execute each command sequentially
	for _, cmd := range commands {
		select {
		case <-ctx.Done():
			return fmt.Errorf("deployment cancelled: %w", ctx.Err())
		default:
		}

		s.logger.Verbose("Executing command %d...", cmd.ordinal)

		_, err = conn.Exec(ctx, cmd.sql)
		if err != nil {
			// Show preview of failed command for debugging
			preview := cmd.sql
			if len(preview) > pgmi.MaxErrorPreviewLength {
				preview = preview[:pgmi.MaxErrorPreviewLength] + "..."
			}
			return fmt.Errorf("command %d execution failed (%s): %w",
				cmd.ordinal, preview, errors.Join(pgmi.ErrExecutionFailed, err))
		}
	}

	s.logger.Info("✓ Executed %d command(s) successfully", len(commands))
	return nil
}

// handleOverwrite handles the database drop and recreate workflow.
func (s *DeploymentService) handleOverwrite(ctx context.Context, connConfig *pgmi.ConnectionConfig, config pgmi.DeploymentConfig) error {
	// Use maintenance database from config (set by CLI layer)
	managementDB := config.MaintenanceDatabase
	if managementDB == "" {
		managementDB = pgmi.DefaultManagementDB // "postgres"
	}

	s.logger.Verbose("Connecting to management database '%s'", managementDB)

	// Create connection config for management database
	mgmtConfig := *connConfig
	mgmtConfig.Database = managementDB

	// Create connector for management database
	connector, err := s.connectorFactory(&mgmtConfig)
	if err != nil {
		return fmt.Errorf("failed to create connector: %w", err)
	}

	// Connect to management database
	pool, err := connector.Connect(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to management database: %w", err)
	}
	defer pool.Close()

	// Wrap pool with adapter to implement DBConnection interface
	dbConn := db.NewPoolAdapter(pool)

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
	// Use maintenance database from config (set by CLI layer)
	managementDB := config.MaintenanceDatabase
	if managementDB == "" {
		managementDB = pgmi.DefaultManagementDB // "postgres"
	}

	s.logger.Verbose("Connecting to management database '%s' to check if target database exists", managementDB)

	// Create connection config for management database
	mgmtConfig := *connConfig
	mgmtConfig.Database = managementDB

	// Create connector for management database
	connector, err := s.connectorFactory(&mgmtConfig)
	if err != nil {
		return fmt.Errorf("failed to create connector: %w", err)
	}

	// Connect to management database
	pool, err := connector.Connect(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to management database: %w", err)
	}
	defer pool.Close()

	// Wrap pool with adapter to implement DBConnection interface
	dbConn := db.NewPoolAdapter(pool)

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

// ExecuteTests executes tests using the provided configuration.
// This method reuses deployment infrastructure for session initialization,
// then directly executes tests from pg_temp.pgmi_unittest_plan.
func (s *DeploymentService) ExecuteTests(ctx context.Context, config pgmi.TestConfig) error {
	// Validate configuration
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	s.logger.Verbose("Starting test execution on database '%s'", config.DatabaseName)
	s.logger.Verbose("Source path: %s", config.SourcePath)
	s.logger.Verbose("Filter pattern: %s", config.FilterPattern)


	// Parse connection string
	connConfig, err := s.parseConnectionString(config.ConnectionString)
	if err != nil {
		return fmt.Errorf("failed to parse connection string: %w", err)
	}

	// Copy Azure credentials from TestConfig (resolved by CLI layer)
	connConfig.AuthMethod = config.AuthMethod
	connConfig.AzureTenantID = config.AzureTenantID
	connConfig.AzureClientID = config.AzureClientID
	connConfig.AzureClientSecret = config.AzureClientSecret

	// Set application name
	if connConfig.AppName == "" {
		connConfig.AppName = "pgmi-test"
	}

	// Prepare test session (scan files, connect to database, load session tables)
	// SessionManager handles: file scanning, database connection, utility functions, files, params, unittest framework
	s.logger.Info("Initializing test session...")
	targetConfig := *connConfig
	targetConfig.Database = config.DatabaseName
	session, err := s.sessionManager.PrepareSession(ctx, &targetConfig, config.SourcePath, config.Parameters, config.Verbose)
	if err != nil {
		return err // Error already wrapped by SessionManager
	}
	defer session.Close()

	// List mode: show tests and exit
	if config.ListOnly {
		return s.listTests(ctx, session.Conn(), config.FilterPattern)
	}

	// Execute tests from pg_temp.pgmi_unittest_plan
	s.logger.Info("Executing tests...")
	if err := s.executeTestPlan(ctx, session.Conn(), config.FilterPattern); err != nil {
		return fmt.Errorf("test execution failed: %w", err)
	}

	s.logger.Info("✓ All tests passed")
	return nil
}

// listTests prints the filtered test execution plan without running tests.
func (s *DeploymentService) listTests(ctx context.Context, conn *pgxpool.Conn, filterPattern string) error {
	rows, err := conn.Query(ctx, queryTestPlanList, filterPattern)
	if err != nil {
		return fmt.Errorf("failed to query test plan: %w", err)
	}
	defer rows.Close()

	fmt.Fprintf(os.Stderr, "\nDiscovered tests (filter: %q):\n\n", filterPattern)

	testCount := 0
	setupCount := 0
	teardownCount := 0

	for rows.Next() {
		var ordinal int
		var stepType, scriptPath string
		if err := rows.Scan(&ordinal, &stepType, &scriptPath); err != nil {
			return fmt.Errorf("failed to scan test row: %w", err)
		}

		// Format output
		stepLabel := "Test"
		switch stepType {
		case "setup":
			stepLabel = "Setup"
			setupCount++
		case "teardown":
			stepLabel = "Teardown"
			teardownCount++
		case "test":
			testCount++
		}

		fmt.Fprintf(os.Stderr, "%d. %-8s %s\n", ordinal, stepLabel+":", scriptPath)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error reading test rows: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\nTotal: %d tests (with %d setup, %d teardown)\n", testCount, setupCount, teardownCount)
	return nil
}

// executeTestPlan queries pg_temp.pgmi_unittest_pvw_plan and executes tests sequentially.
// Fails immediately on first test failure (PostgreSQL native fail-fast).
func (s *DeploymentService) executeTestPlan(ctx context.Context, conn *pgxpool.Conn, filterPattern string) error {
	// Start a transaction for savepoint support (using SQL BEGIN/ROLLBACK)
	_, err := conn.Exec(ctx, "BEGIN")
	if err != nil {
		return fmt.Errorf("failed to begin transaction for tests: %w", err)
	}
	// Ensure we rollback on exit (tests should have no side effects)
	defer func() { _, _ = conn.Exec(ctx, "ROLLBACK") }()

	// Query filtered test plan and read all into memory first
	rows, err := conn.Query(ctx, queryTestPlan, filterPattern)
	if err != nil {
		return fmt.Errorf("failed to query test plan: %w", err)
	}

	// Read all test steps into memory first, then close the query
	type testStep struct {
		ordinal    int
		stepType   string
		scriptPath string
		sql        string
	}
	var steps []testStep

	for rows.Next() {
		var step testStep
		if err := rows.Scan(&step.ordinal, &step.stepType, &step.scriptPath, &step.sql); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan test row: %w", err)
		}
		steps = append(steps, step)
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error reading test rows: %w", err)
	}

	// Now execute each test step sequentially
	// PostgreSQL will stop on first error (native fail-fast)
	for _, step := range steps {
		select {
		case <-ctx.Done():
			return fmt.Errorf("test execution cancelled: %w", ctx.Err())
		default:
		}

		_, err := conn.Exec(ctx, step.sql)
		if err != nil {
			// PostgreSQL already printed the error context via RAISE NOTICE/EXCEPTION
			// We just add minimal pgmi context and exit
			return fmt.Errorf("test failed in %s: %w", step.scriptPath, err)
		}
	}

	return nil
}
