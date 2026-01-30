package services

import (
	"context"
	"errors"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func validDeps() (
	func(*pgmi.ConnectionConfig) (pgmi.Connector, error),
	pgmi.Approver,
	pgmi.Logger,
	*SessionManager,
	pgmi.FileScanner,
	pgmi.DatabaseManager,
) {
	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return &mockConnector{}, nil
	}
	sm := NewSessionManager(connFactory, &mockFileScanner{}, &mockFileLoader{}, &mockLogger{})
	return connFactory, &mockApprover{}, &mockLogger{}, sm, &mockFileScanner{}, &mockDatabaseManager{}
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

func TestExecuteTests_InvalidConfig(t *testing.T) {
	cf, ap, lg, sm, fs, dm := validDeps()
	svc := NewDeploymentService(cf, ap, lg, sm, fs, dm)

	err := svc.ExecuteTests(context.Background(), pgmi.TestConfig{})
	if err == nil {
		t.Fatal("Expected error")
	}
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Errorf("Expected ErrInvalidConfig, got: %v", err)
	}
}

func TestExecuteTests_InvalidConnectionString(t *testing.T) {
	cf, ap, lg, sm, fs, dm := validDeps()
	svc := NewDeploymentService(cf, ap, lg, sm, fs, dm)

	err := svc.ExecuteTests(context.Background(), pgmi.TestConfig{
		SourcePath:       "/src",
		DatabaseName:     "db",
		ConnectionString: "not-valid",
	})

	if err == nil {
		t.Fatal("Expected error for invalid connection string")
	}
}
