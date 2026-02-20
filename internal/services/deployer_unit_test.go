package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

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
	mgmtConn managementDBConnFunc,
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

func successfulMgmtConn() managementDBConnFunc {
	return func(_ context.Context, _ *pgmi.ConnectionConfig, _ string) (pgmi.DBConnection, func(), error) {
		return &mockDBConnection{}, noop, nil
	}
}

func failingMgmtConn(err error) managementDBConnFunc {
	return func(_ context.Context, _ *pgmi.ConnectionConfig, _ string) (pgmi.DBConnection, func(), error) {
		return nil, nil, err
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

// --- Overwrite workflow tests ---

func TestDeploy_OverwriteDBNotExists_Creates(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: false}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true

	err := svc.Deploy(context.Background(), cfg)
	if err == nil || err.Error() != "mock stop" {
		t.Fatalf("Expected 'mock stop', got: %v", err)
	}
}

func TestDeploy_OverwriteApproved_FullCycle(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	approver := &mockApprover{approved: true}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	svc := newTestService(dbMgr, approver, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true

	err := svc.Deploy(context.Background(), cfg)
	if err == nil || err.Error() != "mock stop" {
		t.Fatalf("Expected 'mock stop', got: %v", err)
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
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	err := svc.Deploy(context.Background(), validConfig())
	if err == nil || err.Error() != "mock stop" {
		t.Fatalf("Expected 'mock stop', got: %v", err)
	}
}

func TestDeploy_EnsureDBCreates(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: false}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	err := svc.Deploy(context.Background(), validConfig())
	if err == nil || err.Error() != "mock stop" {
		t.Fatalf("Expected 'mock stop', got: %v", err)
	}
}

func TestDeploy_EnsureDBCheckFails(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsErr: fmt.Errorf("check failed")}
	svc := newTestService(dbMgr, nil, nil, successfulMgmtConn())

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
	svc := newTestService(dbMgr, nil, nil, successfulMgmtConn())

	err := svc.Deploy(context.Background(), validConfig())
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "create") {
		t.Fatalf("Expected create error, got: %v", err)
	}
}

// --- Management connector failure tests ---

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

func TestDeploy_ReadDeploySQLFails(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	fileScanner := &mockFileScanner{readErr: fmt.Errorf("deploy.sql not found: %w", pgmi.ErrDeploySQLNotFound)}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	cf, _, lg, _, _, _ := validDeps()
	svc := NewDeploymentService(cf, &mockApprover{}, lg, sessPreparer, fileScanner, dbMgr)
	svc.mgmtConnector = successfulMgmtConn()

	err := svc.Deploy(context.Background(), validConfig())

	if err == nil || !strings.Contains(err.Error(), "mock stop") {
		t.Fatalf("Expected mock stop (session prep comes first), got: %v", err)
	}
}

func TestDeploy_MaintenanceDBDefault(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.MaintenanceDatabase = ""

	err := svc.Deploy(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "mock stop") {
		t.Fatalf("Expected mock stop, got: %v", err)
	}
}

func TestDeploy_CustomMaintenanceDB(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}

	var capturedDB string
	customMgmt := func(_ context.Context, _ *pgmi.ConnectionConfig, dbName string) (pgmi.DBConnection, func(), error) {
		capturedDB = dbName
		return &mockDBConnection{}, noop, nil
	}

	svc := newTestService(dbMgr, nil, sessPreparer, customMgmt)

	cfg := validConfig()
	cfg.MaintenanceDatabase = "custom_maint"

	_ = svc.Deploy(context.Background(), cfg)
	if capturedDB != "custom_maint" {
		t.Fatalf("Expected maintenance DB 'custom_maint', got: %q", capturedDB)
	}
}

func TestDeploy_OverwriteCustomMaintenanceDB(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: false}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}

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

func TestDeploy_OverwriteBlocksManagementDB(t *testing.T) {
	svc := newTestService(nil, &mockApprover{approved: true}, nil, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true
	cfg.DatabaseName = "postgres" // same as DefaultManagementDB

	err := svc.Deploy(context.Background(), cfg)
	if err == nil {
		t.Fatal("Expected error when overwriting management database")
	}
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Errorf("Expected ErrInvalidConfig, got: %v", err)
	}
	if !strings.Contains(err.Error(), "management database") {
		t.Errorf("Error should mention management database, got: %v", err)
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

func TestDeploy_OverwriteBlocksCustomManagementDB(t *testing.T) {
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
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true
	cfg.Force = true
	cfg.DatabaseName = "myapp" // different from management DB

	err := svc.Deploy(context.Background(), cfg)
	// Should pass validation and proceed (mock stop from session prep)
	if err == nil || err.Error() != "mock stop" {
		t.Fatalf("Expected 'mock stop' (passed validation), got: %v", err)
	}
}

func TestValidateOverwriteTarget(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		mgmtDB   string
		wantErr   bool
		errSubstr string
	}{
		{"different DB is fine", "myapp", "postgres", false, ""},
		{"same as management DB", "postgres", "postgres", true, "management"},
		{"case-insensitive management", "POSTGRES", "postgres", true, "management"},
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


