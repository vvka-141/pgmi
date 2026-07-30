package services

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var errMockStop = errors.New("mock stop")

func validDeps() (
	func(*pgmi.ConnectionConfig) (pgmi.Connector, error),
	pgmi.Approver,
	pgmi.Logger,
	pgmi.SessionPreparer,
	pgmi.FileScanner,
	pgmi.DatabaseManager,
) {
	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return &mockConnector{}, nil
	}
	return connFactory, &mockApprover{}, &mockLogger{}, &mockSessionPreparer{}, &mockFileScanner{}, &mockDatabaseManager{}
}

func validConfig() pgmi.DeploymentConfig {
	return pgmi.DeploymentConfig{
		SourcePath:       "/src",
		DatabaseName:     "testdb",
		ConnectionString: "postgresql://localhost/postgres",
	}
}

func newTestService(
	dbMgr *mockDatabaseManager,
	approver *mockApprover,
	sessPreparer *mockSessionPreparer,
	mgmtConn maintenanceDBConnFunc,
) *DeploymentService {
	cf, _, lg, _, fs, _ := validDeps()
	if approver == nil {
		approver = &mockApprover{}
	}
	if sessPreparer == nil {
		sessPreparer = &mockSessionPreparer{}
	}
	if dbMgr == nil {
		dbMgr = &mockDatabaseManager{}
	}
	svc := NewDeploymentService(cf, approver, lg, sessPreparer, fs, dbMgr)
	if mgmtConn != nil {
		svc.mgmtConnector = mgmtConn
	}
	return svc
}

func noop() {}

func successfulMgmtConn() maintenanceDBConnFunc {
	return func(_ context.Context, _ *pgmi.ConnectionConfig, _ string) (pgmi.DBConnection, func(), error) {
		return &mockDBConnection{}, noop, nil
	}
}

func failingMgmtConn(err error) maintenanceDBConnFunc {
	return func(_ context.Context, _ *pgmi.ConnectionConfig, _ string) (pgmi.DBConnection, func(), error) {
		return nil, nil, err
	}
}

// missingTargetMgmtConn simulates a target database that does not exist: the
// probe gets PostgreSQL's invalid_catalog_name, every other database connects.
// Dialed database names are appended to dialed in call order.
func missingTargetMgmtConn(targetDB string, dialed *[]string) maintenanceDBConnFunc {
	return func(_ context.Context, _ *pgmi.ConnectionConfig, dbName string) (pgmi.DBConnection, func(), error) {
		*dialed = append(*dialed, dbName)
		if dbName == targetDB {
			return nil, nil, &pgconn.PgError{Code: "3D000", Message: `database "` + targetDB + `" does not exist`}
		}
		return &mockDBConnection{}, noop, nil
	}
}

func TestNewDeploymentService_NilDeps(t *testing.T) {
	cf, ap, lg, sm, fs, dm := validDeps()

	tests := []struct {
		name string
		fn   func()
	}{
		{"nil connectorFactory", func() { NewDeploymentService(nil, ap, lg, sm, fs, dm) }},
		{"nil approver", func() { NewDeploymentService(cf, nil, lg, sm, fs, dm) }},
		{"nil logger", func() { NewDeploymentService(cf, ap, nil, sm, fs, dm) }},
		{"nil sessionManager", func() { NewDeploymentService(cf, ap, lg, nil, fs, dm) }},
		{"nil fileScanner", func() { NewDeploymentService(cf, ap, lg, sm, nil, dm) }},
		{"nil dbManager", func() { NewDeploymentService(cf, ap, lg, sm, fs, nil) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Error("Expected panic")
				}
			}()
			tt.fn()
		})
	}
}

func TestDeploy_InvalidConfig(t *testing.T) {
	cf, ap, lg, sm, fs, dm := validDeps()
	svc := NewDeploymentService(cf, ap, lg, sm, fs, dm)
	ctx := context.Background()

	tests := []struct {
		name   string
		config pgmi.DeploymentConfig
	}{
		{"missing SourcePath", pgmi.DeploymentConfig{DatabaseName: "db", ConnectionString: "postgresql://localhost/db"}},
		{"missing DatabaseName", pgmi.DeploymentConfig{SourcePath: "/src", ConnectionString: "postgresql://localhost/db"}},
		{"missing ConnectionString", pgmi.DeploymentConfig{SourcePath: "/src", DatabaseName: "db"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.Deploy(ctx, tt.config)
			if err == nil {
				t.Fatal("Expected error")
			}
			if !errors.Is(err, pgmi.ErrInvalidConfig) {
				t.Errorf("Expected ErrInvalidConfig, got: %v", err)
			}
		})
	}
}

func TestDeploy_InvalidConnectionString(t *testing.T) {
	cf, ap, lg, sm, fs, dm := validDeps()
	svc := NewDeploymentService(cf, ap, lg, sm, fs, dm)

	err := svc.Deploy(context.Background(), pgmi.DeploymentConfig{
		SourcePath:       "/src",
		DatabaseName:     "db",
		ConnectionString: "not-a-valid-connection-string",
	})

	if err == nil {
		t.Fatal("Expected error for invalid connection string")
	}
}

func TestDeploy_MissingDeploySQL_FailsBeforeConnecting(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("overwrite=%v", overwrite), func(t *testing.T) {
			cf, ap, lg, _, fs, dm := validDeps()
			sm := &mockSessionPreparer{scanErr: fmt.Errorf("%w in /src", pgmi.ErrDeploySQLNotFound)}
			svc := NewDeploymentService(cf, ap, lg, sm, fs, dm)

			connected := false
			svc.mgmtConnector = func(_ context.Context, _ *pgmi.ConnectionConfig, _ string) (pgmi.DBConnection, func(), error) {
				connected = true
				return &mockDBConnection{}, noop, nil
			}

			cfg := validConfig()
			cfg.Overwrite = overwrite
			cfg.Force = overwrite

			err := svc.Deploy(context.Background(), cfg)
			if !errors.Is(err, pgmi.ErrDeploySQLNotFound) {
				t.Fatalf("Expected ErrDeploySQLNotFound, got: %v", err)
			}
			if connected {
				t.Error("Deploy connected to the server before validating the source path")
			}
		})
	}
}

// --- Overwrite workflow tests ---

func TestDeploy_OverwriteDBNotExists_Creates(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: false}
	sessPreparer := &mockSessionPreparer{err: errMockStop}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true

	err := svc.Deploy(context.Background(), cfg)
	if !errors.Is(err, errMockStop) {
		t.Fatalf("Expected errMockStop, got: %v", err)
	}
}

func TestDeploy_OverwriteApproved_FullCycle(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	approver := &mockApprover{approved: true}
	sessPreparer := &mockSessionPreparer{err: errMockStop}
	svc := newTestService(dbMgr, approver, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true

	err := svc.Deploy(context.Background(), cfg)
	if !errors.Is(err, errMockStop) {
		t.Fatalf("Expected errMockStop, got: %v", err)
	}
}

func TestDeploy_OverwriteDenied(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	approver := &mockApprover{approved: false}
	svc := newTestService(dbMgr, approver, nil, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true

	err := svc.Deploy(context.Background(), cfg)
	if !errors.Is(err, pgmi.ErrApprovalDenied) {
		t.Fatalf("Expected ErrApprovalDenied, got: %v", err)
	}

	// The error is the signal; not dropping the database is the point. A deploy
	// that dropped first and reported the denial afterwards satisfies the check
	// above and destroys the database the approver was asked about.
	if len(dbMgr.dropped) > 0 {
		t.Errorf("approval was denied but the database was dropped anyway: %v", dbMgr.dropped)
	}
}

func TestDeploy_OverwriteTerminateFails(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true, terminateErr: fmt.Errorf("terminate failed")}
	approver := &mockApprover{approved: true}
	svc := newTestService(dbMgr, approver, nil, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true

	err := svc.Deploy(context.Background(), cfg)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "terminate") {
		t.Fatalf("Expected terminate error, got: %v", err)
	}
}

func TestDeploy_OverwriteDropFails(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true, dropErr: fmt.Errorf("drop failed")}
	approver := &mockApprover{approved: true}
	svc := newTestService(dbMgr, approver, nil, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true

	err := svc.Deploy(context.Background(), cfg)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "drop") {
		t.Fatalf("Expected drop error, got: %v", err)
	}
}

func TestDeploy_OverwriteCreateFails(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true, createErr: fmt.Errorf("create failed")}
	approver := &mockApprover{approved: true}
	svc := newTestService(dbMgr, approver, nil, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true

	err := svc.Deploy(context.Background(), cfg)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "create") {
		t.Fatalf("Expected create error, got: %v", err)
	}
}

// --- ensureDatabaseExists tests ---

func TestDeploy_EnsureDBExists(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: errMockStop}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	err := svc.Deploy(context.Background(), validConfig())
	if !errors.Is(err, errMockStop) {
		t.Fatalf("Expected errMockStop, got: %v", err)
	}
}

func TestDeploy_EnsureDBCreates(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: false}
	sessPreparer := &mockSessionPreparer{err: errMockStop}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	err := svc.Deploy(context.Background(), validConfig())
	if !errors.Is(err, errMockStop) {
		t.Fatalf("Expected errMockStop, got: %v", err)
	}
}

func TestDeploy_EnsureDBCheckFails(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsErr: fmt.Errorf("check failed")}
	var dialed []string
	svc := newTestService(dbMgr, nil, nil, missingTargetMgmtConn("testdb", &dialed))

	err := svc.Deploy(context.Background(), validConfig())
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "check") {
		t.Fatalf("Expected check error, got: %v", err)
	}
}

func TestDeploy_EnsureDBCreateFails(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: false, createErr: fmt.Errorf("create failed")}
	var dialed []string
	svc := newTestService(dbMgr, nil, nil, missingTargetMgmtConn("testdb", &dialed))

	err := svc.Deploy(context.Background(), validConfig())
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "create") {
		t.Fatalf("Expected create error, got: %v", err)
	}
}

// --- Maintenance connector failure tests ---

func TestDeploy_MgmtConnectorFails_Overwrite(t *testing.T) {
	svc := newTestService(nil, nil, nil, failingMgmtConn(fmt.Errorf("conn refused")))

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true

	err := svc.Deploy(context.Background(), cfg)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "conn refused") {
		t.Fatalf("Expected conn refused error, got: %v", err)
	}
}

func TestDeploy_MgmtConnectorFails_Ensure(t *testing.T) {
	svc := newTestService(nil, nil, nil, failingMgmtConn(fmt.Errorf("conn refused")))

	err := svc.Deploy(context.Background(), validConfig())
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "conn refused") {
		t.Fatalf("Expected conn refused error, got: %v", err)
	}
}

// --- Session prep failure tests ---

func TestDeploy_PrepareSessionFails(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("session prep failed")}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	err := svc.Deploy(context.Background(), validConfig())
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "session prep failed") {
		t.Fatalf("Expected session prep error, got: %v", err)
	}
}

// Session preparation runs before deploy.sql is read, so a scanner read error
// is never reached: the session-prep error surfaces first.
func TestDeploy_SessionPrepPrecedesDeploySQLRead(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	fileScanner := &mockFileScanner{readErr: fmt.Errorf("deploy.sql not found: %w", pgmi.ErrDeploySQLNotFound)}
	sessPreparer := &mockSessionPreparer{err: errMockStop}
	cf, _, lg, _, _, _ := validDeps()
	svc := NewDeploymentService(cf, &mockApprover{}, lg, sessPreparer, fileScanner, dbMgr)
	svc.mgmtConnector = successfulMgmtConn()

	err := svc.Deploy(context.Background(), validConfig())

	if !errors.Is(err, errMockStop) {
		t.Fatalf("Expected errMockStop (session prep comes first), got: %v", err)
	}
}

func TestDeploy_MaintenanceDBDefault(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: errMockStop}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.MaintenanceDatabase = ""

	err := svc.Deploy(context.Background(), cfg)
	if !errors.Is(err, errMockStop) {
		t.Fatalf("Expected errMockStop, got: %v", err)
	}
}

func TestDeploy_CustomMaintenanceDB(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: errMockStop}

	var dialed []string
	svc := newTestService(dbMgr, nil, sessPreparer, missingTargetMgmtConn("testdb", &dialed))

	cfg := validConfig()
	cfg.MaintenanceDatabase = "custom_maint"

	_ = svc.Deploy(context.Background(), cfg)
	if len(dialed) != 2 {
		t.Fatalf("Expected a probe of the target followed by the maintenance fallback, got %v", dialed)
	}
	if dialed[1] != "custom_maint" {
		t.Fatalf("Expected maintenance DB 'custom_maint', got: %q", dialed[1])
	}
}

func TestDeploy_OverwriteCustomMaintenanceDB(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: false}
	sessPreparer := &mockSessionPreparer{err: errMockStop}

	var capturedDB string
	customMgmt := func(_ context.Context, _ *pgmi.ConnectionConfig, dbName string) (pgmi.DBConnection, func(), error) {
		capturedDB = dbName
		return &mockDBConnection{}, noop, nil
	}

	svc := newTestService(dbMgr, nil, sessPreparer, customMgmt)

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true
	cfg.MaintenanceDatabase = "maint_db"

	_ = svc.Deploy(context.Background(), cfg)
	if capturedDB != "maint_db" {
		t.Fatalf("Expected maintenance DB 'maint_db', got: %q", capturedDB)
	}
}

// --- Overwrite target validation tests ---

func TestDeploy_OverwriteBlocksMaintenanceDB(t *testing.T) {
	svc := newTestService(nil, &mockApprover{approved: true}, nil, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true
	cfg.DatabaseName = "postgres" // same as DefaultMaintenanceDB

	err := svc.Deploy(context.Background(), cfg)
	if err == nil {
		t.Fatal("Expected error when overwriting maintenance database")
	}
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Errorf("Expected ErrInvalidConfig, got: %v", err)
	}
	if !strings.Contains(err.Error(), "maintenance database") {
		t.Errorf("Error should mention maintenance database, got: %v", err)
	}
}

func TestDeploy_OverwriteBlocksTemplateDatabases(t *testing.T) {
	for _, tmplDB := range []string{"template0", "template1"} {
		t.Run(tmplDB, func(t *testing.T) {
			svc := newTestService(nil, &mockApprover{approved: true}, nil, successfulMgmtConn())

			cfg := validConfig()
			cfg.Overwrite = true
			cfg.Force = true
			cfg.DatabaseName = tmplDB

			err := svc.Deploy(context.Background(), cfg)
			if err == nil {
				t.Fatal("Expected error when overwriting template database")
			}
			if !errors.Is(err, pgmi.ErrInvalidConfig) {
				t.Errorf("Expected ErrInvalidConfig, got: %v", err)
			}
			if !strings.Contains(err.Error(), "template") {
				t.Errorf("Error should mention template, got: %v", err)
			}
		})
	}
}

func TestDeploy_OverwriteBlocksCustomMaintenanceDB(t *testing.T) {
	svc := newTestService(nil, &mockApprover{approved: true}, nil, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true
	cfg.DatabaseName = "maint_db"
	cfg.MaintenanceDatabase = "maint_db" // target == custom maintenance DB

	err := svc.Deploy(context.Background(), cfg)
	if err == nil {
		t.Fatal("Expected error when target equals maintenance database")
	}
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Errorf("Expected ErrInvalidConfig, got: %v", err)
	}
}

func TestDeploy_OverwriteAllowsDifferentDB(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: false}
	sessPreparer := &mockSessionPreparer{err: errMockStop}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true
	cfg.DatabaseName = "myapp" // different from maintenance DB

	err := svc.Deploy(context.Background(), cfg)
	// Should pass validation and proceed (mock stop from session prep)
	if !errors.Is(err, errMockStop) {
		t.Fatalf("Expected errMockStop (passed validation), got: %v", err)
	}
}

func TestValidateOverwriteTarget(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		mgmtDB    string
		wantErr   bool
		errSubstr string
	}{
		{"different DB is fine", "myapp", "postgres", false, ""},
		{"same as maintenance DB", "postgres", "postgres", true, "maintenance"},
		{"case-insensitive maintenance", "POSTGRES", "postgres", true, "maintenance"},
		{"template0", "template0", "postgres", true, "template"},
		{"template1", "template1", "postgres", true, "template"},
		{"TEMPLATE0 case-insensitive", "TEMPLATE0", "postgres", true, "template"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOverwriteTarget(tt.target, tt.mgmtDB)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateOverwriteTarget(%q, %q) error = %v, wantErr %v", tt.target, tt.mgmtDB, err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), tt.errSubstr) {
				t.Errorf("expected error containing %q, got: %v", tt.errSubstr, err)
			}
		})
	}
}

// Two pgmi runs starting together race on CREATE DATABASE, because the advisory
// lock that serialises deploys lives inside the session and the database work
// happens before any session exists. The loser used to receive PostgreSQL's
// catalog error verbatim and exit 1:
//
//	failed to ensure database exists: failed to create database: failed to create
//	database "x": ERROR: duplicate key value violates unique constraint
//	"pg_database_datname_index" (SQLSTATE 23505)
//
// An internal index name in place of a diagnosis, and an exit code CI cannot
// tell apart from a real failure.
func TestDeploy_LostCreateRaceIsAConcurrentDeploy(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"duplicate_database", "42P04"},
		{"unique_violation on pg_database", "23505"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dialed []string
			dbMgr := &mockDatabaseManager{
				createErr: &pgconn.PgError{Code: tc.code, Message: "database already exists"},
			}
			svc := newTestService(dbMgr, nil, nil, missingTargetMgmtConn("testdb", &dialed))

			err := svc.Deploy(context.Background(), validConfig())
			if err == nil {
				t.Fatal("a failed CREATE DATABASE must not succeed")
			}
			if !errors.Is(err, pgmi.ErrConcurrentDeploy) {
				t.Errorf("got %v, want an ErrConcurrentDeploy chain", err)
			}
			if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConcurrentDeploy {
				t.Errorf("exit code %d, want %d -- CI retries on 15, not on 1",
					got, pgmi.ExitConcurrentDeploy)
			}
			if strings.Contains(err.Error(), "pg_database_datname_index") {
				t.Errorf("the catalog index name reached the user: %v", err)
			}
		})
	}
}

// A CREATE failure that is not a collision keeps its own cause, and is not
// mislabelled as someone else's deploy.
func TestDeploy_CreateFailureOtherThanCollisionIsNotConcurrent(t *testing.T) {
	var dialed []string
	dbMgr := &mockDatabaseManager{
		createErr: &pgconn.PgError{Code: "42501", Message: "permission denied to create database"},
	}
	svc := newTestService(dbMgr, nil, nil, missingTargetMgmtConn("testdb", &dialed))

	err := svc.Deploy(context.Background(), validConfig())
	if err == nil {
		t.Fatal("expected the permission error to surface")
	}
	if errors.Is(err, pgmi.ErrConcurrentDeploy) {
		t.Errorf("a permission failure was reported as a concurrent deploy: %v", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the original cause was lost: %v", err)
	}
}

// db/manager already names both the operation and the database in its errors
// ("failed to drop database %q: ..."). The deployer wrapped each call again
// with a prefix saying the same thing, so a real failure read:
//
//	overwrite workflow failed: failed to drop database: failed to drop database
//	"tmpl_probe": ERROR: cannot drop a template database (SQLSTATE 42809)
//
// Reproduced against PG 17.10 by marking a database IS_TEMPLATE, which makes
// DROP fail deterministically. The mocks below return errors shaped the way the
// real manager shapes them, so the doubling is visible without a server.
func TestDeploy_ManagerErrorsAreNotWrappedTwice(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "42809", Message: "cannot drop a template database"}

	for _, tc := range []struct {
		name      string
		mgr       *mockDatabaseManager
		overwrite bool
		phrase    string
	}{
		{
			name: "drop",
			mgr: &mockDatabaseManager{existsResult: true,
				dropErr: fmt.Errorf("failed to drop database %q: %w", "testdb", pgErr)},
			overwrite: true,
			phrase:    "failed to drop database",
		},
		{
			name: "terminate",
			mgr: &mockDatabaseManager{existsResult: true,
				terminateErr: fmt.Errorf("failed to terminate connections to database %q: %w", "testdb", pgErr)},
			overwrite: true,
			phrase:    "failed to terminate connections",
		},
		{
			name: "exists",
			mgr: &mockDatabaseManager{
				existsErr: fmt.Errorf("failed to check existence of database %q: %w", "testdb", pgErr)},
			phrase: "failed to check existence of database",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dialed []string
			mgmt := successfulMgmtConn()
			if !tc.overwrite {
				mgmt = missingTargetMgmtConn("testdb", &dialed)
			}
			svc := newTestService(tc.mgr, &mockApprover{approved: true}, nil, mgmt)

			cfg := validConfig()
			cfg.Overwrite = tc.overwrite
			cfg.Force = tc.overwrite

			err := svc.Deploy(context.Background(), cfg)
			if err == nil {
				t.Fatal("expected the manager failure to surface")
			}
			if n := strings.Count(err.Error(), tc.phrase); n != 1 {
				t.Errorf("%q appears %d times, want 1:\n%v", tc.phrase, n, err)
			}
			// The database name comes from the manager's message, so dropping the
			// outer wrapper must not cost it.
			if !strings.Contains(err.Error(), "testdb") {
				t.Errorf("the database name was lost: %v", err)
			}
		})
	}
}

// --overwrite promises to drop and recreate the target database. A bare CREATE
// DATABASE inherits the server defaults, so overwriting a LATIN1/C database
// produced a UTF8/en_US.utf8 one — encoding changes how bytes are stored, and
// collation changes index ordering and every comparison. Neither is something
// the user asked for by emptying a database.
//
// Confirmed against PG 17.10 before the fix: LATIN1 / C became
// UTF8 / en_US.utf8. After it, both that database and a default one come back
// unchanged.
func TestDeploy_OverwritePreservesEncodingAndLocale(t *testing.T) {
	comment := "important note"
	settings := &pgmi.DatabaseSettings{
		Encoding: "LATIN1", Collate: "C", CType: "C", PreserveLocale: true,
		Owner: "app_owner", ConnectionLimit: 42,
		Options: []string{"statement_timeout=7s"}, Comment: &comment,
	}
	dbMgr := &mockDatabaseManager{existsResult: true, settings: settings}
	sessPreparer := &mockSessionPreparer{err: errMockStop}
	svc := newTestService(dbMgr, &mockApprover{approved: true}, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite, cfg.Force = true, true

	if err := svc.Deploy(context.Background(), cfg); !errors.Is(err, errMockStop) {
		t.Fatalf("expected errMockStop after the recreate, got: %v", err)
	}

	if dbMgr.createdWith == nil {
		t.Fatal("the database was recreated with the server defaults; its encoding and " +
			"collation were silently replaced")
	}
	if !reflect.DeepEqual(dbMgr.createdWith, settings) {
		t.Errorf("recreated as %+v, want %+v", *dbMgr.createdWith, *settings)
	}
}

// A database matching the server defaults has nothing to preserve, and must
// keep taking the plain CREATE DATABASE path so it still inherits template1 —
// specifying settings would force template0 and drop whatever a site installed
// there.
func TestDeploy_OverwriteOfADefaultDatabaseStaysOnTemplate1(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true, settings: nil}
	sessPreparer := &mockSessionPreparer{err: errMockStop}
	svc := newTestService(dbMgr, &mockApprover{approved: true}, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite, cfg.Force = true, true

	if err := svc.Deploy(context.Background(), cfg); !errors.Is(err, errMockStop) {
		t.Fatalf("expected errMockStop, got: %v", err)
	}
	if dbMgr.createdWith != nil {
		t.Errorf("forced template0 for a database that matched the defaults: %+v", *dbMgr.createdWith)
	}
}

// Creating a database that never existed has nothing to preserve either.
func TestDeploy_CreatingAMissingDatabaseUsesServerDefaults(t *testing.T) {
	var dialed []string
	dbMgr := &mockDatabaseManager{}
	sessPreparer := &mockSessionPreparer{err: errMockStop}
	svc := newTestService(dbMgr, nil, sessPreparer, missingTargetMgmtConn("testdb", &dialed))

	if err := svc.Deploy(context.Background(), validConfig()); !errors.Is(err, errMockStop) {
		t.Fatalf("expected errMockStop, got: %v", err)
	}
	if dbMgr.createdWith != nil {
		t.Errorf("a brand new database was given explicit settings: %+v", *dbMgr.createdWith)
	}
}
