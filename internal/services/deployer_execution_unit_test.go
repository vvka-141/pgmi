package services

import (
	"context"
	"fmt"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestDeploy_ReadDeploySQLFails_AfterSessionPrep(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	fileScanner := &mockFileScanner{readErr: fmt.Errorf("deploy.sql not found")}

	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return &mockConnector{}, nil
	}

	sessPreparer := &mockSessionPreparer{
		session: nil,
		err:     fmt.Errorf("session prep reached"),
	}

	svc := NewDeploymentService(connFactory, &mockApprover{}, &mockLogger{}, sessPreparer, fileScanner, dbMgr)
	svc.mgmtConnector = successfulMgmtConn()

	err := svc.Deploy(context.Background(), validConfig())
	if err == nil {
		t.Fatal("Expected error")
	}
	if !containsStr(err.Error(), "session prep reached") {
		t.Fatalf("Expected session prep error, got: %v", err)
	}
}

func TestExecuteTests_SessionPrepSuccess_RequiresRealConn(t *testing.T) {
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	cf, _, lg, _, fs, dm := validDeps()
	svc := NewDeploymentService(cf, &mockApprover{}, lg, sessPreparer, fs, dm)

	err := svc.ExecuteTests(context.Background(), pgmi.TestConfig{
		SourcePath:       "/src",
		DatabaseName:     "testdb",
		ConnectionString: "postgresql://localhost/postgres",
		ListOnly:         true,
	})
	if err == nil {
		t.Fatal("Expected error")
	}
	if !containsStr(err.Error(), "mock stop") {
		t.Fatalf("Expected mock stop, got: %v", err)
	}
}

func TestExecuteTests_AzureCredentialsCopied(t *testing.T) {
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	cf, _, lg, _, fs, dm := validDeps()
	svc := NewDeploymentService(cf, &mockApprover{}, lg, sessPreparer, fs, dm)

	err := svc.ExecuteTests(context.Background(), pgmi.TestConfig{
		SourcePath:       "/src",
		DatabaseName:     "testdb",
		ConnectionString: "postgresql://localhost/postgres",
		AuthMethod:       pgmi.AuthMethodAzureEntraID,
		AzureTenantID:    "tenant-123",
		AzureClientID:    "client-456",
	})
	if err == nil {
		t.Fatal("Expected error")
	}
}

func TestDeploy_AppNameDefault(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	err := svc.Deploy(context.Background(), cfg)
	if err == nil || !containsStr(err.Error(), "mock stop") {
		t.Fatalf("Expected mock stop, got: %v", err)
	}
}

func TestDeploy_OverwriteApproverError(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	approver := &mockApprover{err: fmt.Errorf("approval system down")}
	svc := newTestService(dbMgr, approver, nil, successfulMgmtConn())

	cfg := validConfig()
	cfg.Overwrite = true

	err := svc.Deploy(context.Background(), cfg)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !containsStr(err.Error(), "approval") {
		t.Fatalf("Expected approval error, got: %v", err)
	}
}

func TestDeploy_AuthMethodCopiedToConnConfig(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	cfg.AuthMethod = pgmi.AuthMethodAzureEntraID
	cfg.AzureTenantID = "tenant"
	cfg.AzureClientID = "client"
	cfg.AzureClientSecret = "secret"

	err := svc.Deploy(context.Background(), cfg)
	if err == nil || !containsStr(err.Error(), "mock stop") {
		t.Fatalf("Expected mock stop, got: %v", err)
	}
}

func TestDeploy_EnsureDBExists_MgmtDefaultsToPostgres(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}

	var capturedDB string
	mgmt := func(_ context.Context, _ *pgmi.ConnectionConfig, dbName string) (pgmi.DBConnection, func(), error) {
		capturedDB = dbName
		return &mockDBConnection{}, noop, nil
	}

	svc := newTestService(dbMgr, nil, sessPreparer, mgmt)
	cfg := validConfig()
	cfg.MaintenanceDatabase = ""

	_ = svc.Deploy(context.Background(), cfg)
	if capturedDB != pgmi.DefaultManagementDB {
		t.Fatalf("Expected default management DB %q, got %q", pgmi.DefaultManagementDB, capturedDB)
	}
}
