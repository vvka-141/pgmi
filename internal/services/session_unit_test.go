package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestNewSessionManager_NilDeps(t *testing.T) {
	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return &mockConnector{}, nil
	}

	tests := []struct {
		name string
		fn   func()
	}{
		{"nil connectorFactory", func() {
			NewSessionManager(nil, &mockFileScanner{}, &mockFileLoader{}, &mockLogger{})
		}},
		{"nil fileScanner", func() {
			NewSessionManager(connFactory, nil, &mockFileLoader{}, &mockLogger{})
		}},
		{"nil fileLoader", func() {
			NewSessionManager(connFactory, &mockFileScanner{}, nil, &mockLogger{})
		}},
		{"nil logger", func() {
			NewSessionManager(connFactory, &mockFileScanner{}, &mockFileLoader{}, nil)
		}},
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

func TestPrepareSession_ValidateDeploySQLFails(t *testing.T) {
	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return &mockConnector{}, nil
	}
	scanner := &mockFileScanner{validateErr: fmt.Errorf("deploy.sql missing")}
	sm := NewSessionManager(connFactory, scanner, &mockFileLoader{}, &mockLogger{})

	_, err := sm.PrepareSession(nil, &pgmi.ConnectionConfig{}, "/src", nil, false)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "file scanning failed") {
		t.Errorf("Expected 'file scanning failed' in error, got: %v", err)
	}
}

func TestPrepareSession_ScanDirectoryFails(t *testing.T) {
	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return &mockConnector{}, nil
	}
	scanner := &mockFileScanner{scanErr: fmt.Errorf("permission denied")}
	sm := NewSessionManager(connFactory, scanner, &mockFileLoader{}, &mockLogger{})

	_, err := sm.PrepareSession(nil, &pgmi.ConnectionConfig{}, "/src", nil, false)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "file scanning failed") {
		t.Errorf("Expected 'file scanning failed' in error, got: %v", err)
	}
}

func TestPrepareSession_ConnectorFactoryFails(t *testing.T) {
	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return nil, fmt.Errorf("factory error")
	}
	scanner := &mockFileScanner{}
	sm := NewSessionManager(connFactory, scanner, &mockFileLoader{}, &mockLogger{})

	_, err := sm.PrepareSession(context.Background(), &pgmi.ConnectionConfig{}, "/src", nil, false)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "database connection failed") {
		t.Errorf("Expected 'database connection failed', got: %v", err)
	}
}

func TestPrepareSession_ConnectFails(t *testing.T) {
	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return &mockConnector{err: fmt.Errorf("connection refused")}, nil
	}
	scanner := &mockFileScanner{}
	sm := NewSessionManager(connFactory, scanner, &mockFileLoader{}, &mockLogger{})

	_, err := sm.PrepareSession(context.Background(), &pgmi.ConnectionConfig{}, "/src", nil, false)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "database connection failed") {
		t.Errorf("Expected 'database connection failed', got: %v", err)
	}
}
