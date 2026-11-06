package services

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/contract"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/preprocessor"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

const overwriteRecreateTimeout = 60 * time.Second

type maintenanceDBConnFunc func(ctx context.Context, connConfig *db.ConnectionConfig, dbName string) (db.DBConnection, func(), error)

// DeploymentService runs deployments; pkg/pgmi/deploy wires it.
// Not safe for concurrent Deploy() calls on the same instance.
type DeploymentService struct {
	connectorFactory func(*db.ConnectionConfig) (db.Connector, error)
	approver         pgmi.Approver
	logger           pgmi.Logger
	sessionManager   SessionPreparer
	dbManager        DatabaseManager
	mgmtConnector    maintenanceDBConnFunc
	lastResult       *pgmi.DeployResult
}

// LastResult returns statistics from the most recent Deploy call,
// or nil if Deploy has not been called.
func (s *DeploymentService) LastResult() *pgmi.DeployResult {
	return s.lastResult
}

// NewDeploymentService creates a new DeploymentService with all dependencies injected.
// Panics on nil dependencies (programmer error); returns errors for runtime conditions.
func NewDeploymentService(
	connectorFactory func(*db.ConnectionConfig) (db.Connector, error),
	approver pgmi.Approver,
	logger pgmi.Logger,
	sessionManager SessionPreparer,
	dbManager DatabaseManager,
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
	if dbManager == nil {
		panic("dbManager cannot be nil")
	}

	svc := &DeploymentService{
		connectorFactory: connectorFactory,
		approver:         approver,
		logger:           logger,
		sessionManager:   sessionManager,
		dbManager:        dbManager,
	}
	svc.mgmtConnector = svc.defaultMgmtConnector
	return svc
}

func (s *DeploymentService) defaultMgmtConnector(ctx context.Context, connConfig *db.ConnectionConfig, dbName string) (db.DBConnection, func(), error) {
	mgmtConfig := connConfig.DeepCopy()
	mgmtConfig.Database = dbName

	connector, err := s.connectorFactory(&mgmtConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create connector: %w", err)
	}

	pool, err := connector.Connect(ctx)
	if err != nil {
		closeConnector(connector)
		return nil, nil, fmt.Errorf("connect to %q: %w", dbName, err)
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
	err := s.deploy(ctx, config)
	if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
		return &stoppedError{err: err, cause: ctx.Err()}
	}
	return err
}

// stoppedError keeps the server's 57014 for the location and the context's
// error for the exit code.
type stoppedError struct {
	err, cause error
}

func (e *stoppedError) Error() string   { return e.err.Error() }
func (e *stoppedError) Unwrap() []error { return []error{e.err, e.cause} }

func (s *DeploymentService) deploy(ctx context.Context, config pgmi.DeploymentConfig) error {
	start := time.Now()
	s.lastResult = &pgmi.DeployResult{Database: config.DatabaseName}
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
		return err
	}

	// --compat is resolved entirely offline, so rejecting it here keeps
	// --overwrite from dropping and recreating a database only to fail on a
	// check that never needed the server.
	if _, _, err := contract.Load(config.Compat); err != nil {
		return err
	}
	if err := loader.ValidateParameters(config.Parameters); err != nil {
		return err
	}

	// Handle overwrite workflow if requested (drop and recreate database)
	if config.Overwrite {
		release, err := s.handleOverwrite(ctx, connConfig, config)
		if err != nil {
			return err
		}
		defer release()
	} else {
		// If not overwriting, ensure database exists (create if missing)
		if err := s.ensureDatabaseExists(ctx, connConfig, config); err != nil {
			return err
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
	s.lastResult.Ignored = session.Ignored
	pgConn := session.Conn().Conn().PgConn()
	s.lastResult.ServerVersion = pgConn.ParameterStatus("server_version")
	s.lastResult.TLS = &pgmi.TLSInfo{SSLMode: targetConfig.SSLMode}
	if conn, ok := pgConn.Conn().(*tls.Conn); ok {
		s.lastResult.TLS.Enabled = true
		s.lastResult.TLS.Version = tls.VersionName(conn.ConnectionState().Version)
	}
	s.logger.Verbose("TLS: %s (sslmode=%s)", cmp.Or(s.lastResult.TLS.Version, "none"), targetConfig.SSLMode)

	s.logger.Info("Executing deploy.sql")
	macroCount, err := s.executeDeploySQL(ctx, session.Conn(), scanResult)
	s.lastResult.TestMacros = macroCount
	return err
}

// validateAndParseConfig validates the configuration and parses the connection string.
func (s *DeploymentService) validateAndParseConfig(config pgmi.DeploymentConfig) (*db.ConnectionConfig, error) {
	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, err
	}

	s.logger.Verbose("Deploying to database %q", config.DatabaseName)
	s.logger.Verbose("Source path: %s", config.SourcePath)

	// Parse connection string
	connConfig, err := db.ParseConnectionString(config.ConnectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w: %w", err, pgmi.ErrInvalidConfig)
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
	connConfig.AWSRegion = config.AWSRegion
	connConfig.GoogleInstance = config.GoogleInstance

	// A token must never travel without TLS: the same gate the CLI applies.
	// Only for token auth, so a library deploy with a password still leaves
	// an unset sslmode to the driver, PGSSLMODE included.
	if config.AuthMethod == pgmi.AuthMethodAzureEntraID || config.AuthMethod == pgmi.AuthMethodAWSIAM {
		source := "the connection string"
		if connConfig.SSLMode == "" && os.Getenv("PGSSLMODE") != "" {
			connConfig.SSLMode, source = os.Getenv("PGSSLMODE"), "PGSSLMODE"
		}
		if err := db.ApplySSLMode(connConfig, source); err != nil {
			return nil, err
		}
	}

	return connConfig, nil
}

// executeDeploySQL preprocesses and executes deploy.sql, read and validated
// by ScanProject before anything touched the server.
// Preprocessing expands CALL pgmi_test() macros by querying pgmi_test_plan() from SQL.
// Returns the number of test macros expanded and any error.
func (s *DeploymentService) executeDeploySQL(
	ctx context.Context,
	conn *pgxpool.Conn,
	scan scanner.Result,
) (int, error) {
	// Preprocess: expand CALL pgmi_test() macros by querying pgmi_test_plan() from SQL
	pipeline := preprocessor.NewPipeline()
	result, err := pipeline.Process(ctx, conn, scan.DeploySQL)
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
	committed := 0
	for i, unit := range units {
		if _, err := conn.Exec(ctx, unit); err != nil {
			s.lastResult.UnitsCommitted = committed
			s.lastResult.FailedUnit = i + 1
			s.lastResult.ExecutionMode = executionMode(committed)
			scriptErr := &pgmi.ScriptError{
				Err: err, Name: "deploy.sql", Script: unit, Expanded: result.MacroCount > 0, Files: scan.Files,
				Source: scan.DeploySQL, OriginalLine: result.Lines.OriginalLine, Tail: i > 0,
			}
			return result.MacroCount, fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, scriptErr)
		}
		if conn.Conn().PgConn().TxStatus() == 'I' || (i == 0 && preprocessor.HeadCommits(unit)) {
			committed = i + 1
		}
	}
	s.lastResult.UnitsCommitted = committed

	// Session cleanup rolls back whatever is still open, so a script ending
	// inside BEGIN ... without its COMMIT applied nothing from that block.
	if committed < len(units) {
		s.lastResult.FailedUnit = len(units)
		s.lastResult.ExecutionMode = executionMode(committed)
		return result.MacroCount, fmt.Errorf("%w: deploy.sql ended inside an open transaction, which was rolled back; end it with COMMIT", pgmi.ErrExecutionFailed)
	}

	return result.MacroCount, nil
}

// executionMode names what a failure left behind: "atomic" when nothing
// committed, "psql" when earlier units did.
func executionMode(committed int) string {
	if committed == 0 {
		return "atomic"
	}
	return "psql"
}

func validateOverwriteTarget(targetDB, maintenanceDB string) error {
	if strings.EqualFold(targetDB, maintenanceDB) {
		return fmt.Errorf("cannot overwrite maintenance database %q\npgmi connects to it for CREATE/DROP DATABASE; pick a different target with -d: %w", targetDB, pgmi.ErrInvalidConfig)
	}
	if isTemplateDatabase(targetDB) {
		return fmt.Errorf("cannot drop template database %q (template0/template1 are protected by PostgreSQL): %w", targetDB, pgmi.ErrInvalidConfig)
	}
	return nil
}

// connectMaintenance resolves the maintenance DB name (defaulting to "postgres") and connects.
func (s *DeploymentService) connectMaintenance(ctx context.Context, connConfig *db.ConnectionConfig, maintenanceDB string) (db.DBConnection, func(), error) {
	mgmtDB := maintenanceDB
	if mgmtDB == "" {
		mgmtDB = db.DefaultMaintenanceDB
	}
	s.logger.Verbose("Connecting to maintenance database %q", mgmtDB)
	conn, cleanup, err := s.mgmtConnector(ctx, connConfig, mgmtDB)
	if isUndefinedDatabase(err) {
		return nil, nil, MissingMaintenanceDBError{Name: mgmtDB, Err: err}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("maintenance database: %w", err)
	}
	return conn, cleanup, nil
}

// checkServerVersion refuses a server below pgmi's floor on the maintenance
// connection, before anything is created or dropped, so exit 10 still means
// nothing was touched.
func (s *DeploymentService) checkServerVersion(ctx context.Context, conn db.DBConnection) error {
	num, text, err := s.dbManager.ServerVersion(ctx, conn)
	if err != nil {
		return err
	}
	return verifyServerVersion(num, text)
}

// createIfMissing checks whether the database exists and creates it if not.
// Returns true if the database already existed.
func (s *DeploymentService) createIfMissing(ctx context.Context, dbConn db.DBConnection, dbName string) (existed bool, err error) {
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
		if s.lastResult != nil {
			s.lastResult.Created = true
		}
		return false, nil
	}
	return true, nil
}

// handleOverwrite handles the database drop and recreate workflow.
// handleOverwrite returns a release the caller holds until the deploy ends:
// the overwrite lock is taken before the existence check and stays taken
// until the deploy ends, so a second --overwrite cannot drop what this one
// creates or deploys. Every maintenance step runs on the connection holding
// it, so the deploy needs two connections: that one and the target session.
func (s *DeploymentService) handleOverwrite(ctx context.Context, connConfig *db.ConnectionConfig, config pgmi.DeploymentConfig) (release func(), err error) {
	managementDB := config.MaintenanceDatabase
	if managementDB == "" {
		managementDB = db.DefaultMaintenanceDB
	}

	if err := validateOverwriteTarget(config.DatabaseName, managementDB); err != nil {
		return nil, err
	}

	maintenance, cleanup, err := s.connectMaintenance(ctx, connConfig, managementDB)
	if err != nil {
		return nil, err
	}
	dbConn, unlock, err := s.dbManager.LockOverwrite(ctx, maintenance, config.DatabaseName)
	if err != nil {
		cleanup()
		return nil, err
	}
	if dbConn == nil {
		cleanup()
		return nil, fmt.Errorf("%w: another pgmi --overwrite of %q is running", pgmi.ErrConcurrentDeploy, config.DatabaseName)
	}
	held := func() {
		unlock()
		cleanup()
	}
	defer func() {
		if err != nil {
			held()
		}
	}()

	if err := s.checkServerVersion(ctx, dbConn); err != nil {
		return nil, err
	}
	existed, err := s.createIfMissing(ctx, dbConn, config.DatabaseName)
	if err != nil {
		return nil, err
	}
	if !existed {
		return held, nil
	}

	s.logger.Verbose("Database %q exists; requesting approval", config.DatabaseName)
	approved, err := s.approver.RequestApproval(ctx, config.DatabaseName)
	if errors.Is(err, context.Canceled) {
		return nil, pgmi.WithMessage(err, "overwrite cancelled")
	}
	if err != nil {
		return nil, err
	}
	if !approved {
		return nil, pgmi.ErrApprovalDenied
	}

	// Read the encoding and locale before the database ceases to exist. Without
	// this, --overwrite recreated it from the server defaults, so a LATIN1/C
	// database came back UTF8/en_US.utf8 — a change to how bytes are stored and
	// how every index orders, from a flag that only promises to empty it.
	settings, err := s.dbManager.Settings(ctx, dbConn, config.DatabaseName)
	if err != nil {
		return nil, err
	}
	if settings != nil {
		s.logger.Verbose("Preserving encoding %s, collation %s across the recreate",
			settings.Encoding, settings.Collate)
	}

	gaps, err := s.dbManager.RestoreGaps(ctx, dbConn, config.DatabaseName)
	if err != nil {
		return nil, err
	}
	if len(gaps) > 0 {
		return nil, fmt.Errorf("%w: --overwrite would drop %q and could not restore, as the deploying role:\n  %s\n"+
			"nothing was dropped; deploy as the database owner or a superuser, or remove these first",
			pgmi.ErrInvalidConfig, config.DatabaseName, strings.Join(gaps, "\n  "))
	}

	running, err := s.dbManager.DeployInProgress(ctx, dbConn, config.DatabaseName)
	if err != nil {
		return nil, err
	}
	if running {
		return nil, fmt.Errorf("%w: another pgmi deployment is running against %q; --overwrite would end it",
			pgmi.ErrConcurrentDeploy, config.DatabaseName)
	}

	// DROP and CREATE DATABASE each refuse a transaction block, so the pair
	// cannot be made atomic. Instead the window is made uninterruptible: a
	// cancel arriving after the drop would otherwise leave the database gone,
	// or back without its owner, ACL and options.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	recreateCtx, cancelRecreate := context.WithTimeout(context.WithoutCancel(ctx), overwriteRecreateTimeout)
	defer cancelRecreate()

	s.logger.Verbose("DROP DATABASE %q", config.DatabaseName)
	if err := s.dbManager.Drop(recreateCtx, dbConn, config.DatabaseName); err != nil {
		return nil, err // manager.Drop already names the database
	}

	s.logger.Verbose("CREATE DATABASE %q", config.DatabaseName)
	if err := s.dbManager.Create(recreateCtx, dbConn, config.DatabaseName, settings); err != nil {
		return nil, classifyCreateFailure(err)
	}

	if s.lastResult != nil {
		s.lastResult.Recreated = true
	}
	s.logger.Info("Recreated database %q", config.DatabaseName)
	return held, nil
}

// ensureDatabaseExists ensures the target database exists, creating it if necessary.
func (s *DeploymentService) ensureDatabaseExists(ctx context.Context, connConfig *db.ConnectionConfig, config pgmi.DeploymentConfig) error {
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

	if err := s.checkServerVersion(ctx, dbConn); err != nil {
		return err
	}
	if _, err := s.createIfMissing(ctx, dbConn, config.DatabaseName); err != nil {
		return err
	}
	s.logger.Verbose("Created database %q", config.DatabaseName)

	return nil
}

// isUndefinedDatabase reports whether err is PostgreSQL's invalid_catalog_name,
// the response to connecting to a database that does not exist.
// MissingMaintenanceDBError replaces the connector's "createdb or
// --overwrite" hint, which is right for a missing target and wrong here:
// --overwrite needs this very database to create anything. Its text names
// the library's field; the CLI rewords it for its own flags and pgmi.yaml.
type MissingMaintenanceDBError struct {
	Name string
	Err  error
}

func (e MissingMaintenanceDBError) Error() string {
	return fmt.Sprintf("maintenance database %q does not exist\n"+
		"pgmi connects to it to create or drop the target; set DeploymentConfig.MaintenanceDatabase "+
		"to an existing database (empty means postgres)", e.Name)
}

func (e MissingMaintenanceDBError) Unwrap() []error { return []error{e.Err, pgmi.ErrConnectionFailed} }

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
	if errors.As(err, &pgErr) && pgErr.Code == "42501" {
		return fmt.Errorf("%w: %w; the database does not exist and this role cannot create it: check the -d name, or grant the role CREATEDB",
			pgmi.ErrExecutionFailed, err)
	}
	return err
}

func isTemplateDatabase(name string) bool {
	lower := strings.ToLower(name)
	return lower == "template0" || lower == "template1"
}
