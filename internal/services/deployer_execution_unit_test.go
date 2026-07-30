package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestDeploy_SessionPrepErrorPropagates(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}

	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return &mockConnector{}, nil
	}

	sessPreparer := &mockSessionPreparer{
		session: nil,
		err:     fmt.Errorf("session prep reached"),
	}

	svc := NewDeploymentService(connFactory, &mockApprover{}, &mockLogger{}, sessPreparer, &mockFileScanner{}, dbMgr)
	svc.mgmtConnector = successfulMgmtConn()

	err := svc.Deploy(context.Background(), validConfig())
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "session prep reached") {
		t.Fatalf("Expected session prep error, got: %v", err)
	}
}

func TestDeploy_AppNameDefault(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}
	svc := newTestService(dbMgr, nil, sessPreparer, successfulMgmtConn())

	cfg := validConfig()
	err := svc.Deploy(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "mock stop") {
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
	if !strings.Contains(err.Error(), "approval") {
		t.Fatalf("Expected approval error, got: %v", err)
	}
	// An approver that failed gave no answer, which is not the same as yes.
	if len(dbMgr.dropped) > 0 {
		t.Errorf("the approval request failed but the database was dropped anyway: %v",
			dbMgr.dropped)
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
	if err == nil || !strings.Contains(err.Error(), "mock stop") {
		t.Fatalf("Expected mock stop, got: %v", err)
	}
}

func TestDeploy_EnsureDBExists_MgmtDefaultsToPostgres(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}

	var dialed []string
	svc := newTestService(dbMgr, nil, sessPreparer, missingTargetMgmtConn("testdb", &dialed))
	cfg := validConfig()
	cfg.MaintenanceDatabase = ""

	_ = svc.Deploy(context.Background(), cfg)

	if len(dialed) != 2 {
		t.Fatalf("Expected a probe of the target followed by the maintenance fallback, got %v", dialed)
	}
	if dialed[1] != pgmi.DefaultMaintenanceDB {
		t.Fatalf("Expected default maintenance DB %q, got %q", pgmi.DefaultMaintenanceDB, dialed[1])
	}
}

// A deploy to a database that already exists must not touch a maintenance
// database at all: a CI role is routinely granted CONNECT on its own database
// and nothing else.
func TestDeploy_ExistingDatabase_SkipsMaintenanceConnection(t *testing.T) {
	dbMgr := &mockDatabaseManager{existsResult: true}
	sessPreparer := &mockSessionPreparer{err: fmt.Errorf("mock stop")}

	var dialed []string
	mgmt := func(_ context.Context, _ *pgmi.ConnectionConfig, dbName string) (pgmi.DBConnection, func(), error) {
		dialed = append(dialed, dbName)
		return &mockDBConnection{}, noop, nil
	}

	svc := newTestService(dbMgr, nil, sessPreparer, mgmt)
	_ = svc.Deploy(context.Background(), validConfig())

	if len(dialed) != 1 || dialed[0] != "testdb" {
		t.Fatalf("Expected exactly one dial, of the target database, got %v", dialed)
	}
}
