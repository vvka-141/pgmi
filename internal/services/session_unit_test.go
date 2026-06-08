package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestValidateNoDuplicateScriptIDs(t *testing.T) {
	id1 := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	id2 := uuid.MustParse("22222222-2222-4222-8222-222222222222")

	t.Run("duplicate id is rejected", func(t *testing.T) {
		files := []pgmi.FileMetadata{
			{Path: "./b.sql", Metadata: &pgmi.ScriptMetadata{ID: id1}},
			{Path: "./a.sql", Metadata: &pgmi.ScriptMetadata{ID: id1}},
		}
		err := validateNoDuplicateScriptIDs(files)
		if err == nil {
			t.Fatal("expected error for duplicate id, got nil")
		}
		if !errors.Is(err, pgmi.ErrInvalidConfig) {
			t.Errorf("expected ErrInvalidConfig (exit 10), got: %v", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, id1.String()) || !strings.Contains(msg, "./a.sql") || !strings.Contains(msg, "./b.sql") {
			t.Errorf("error should name the id and both files, got: %s", msg)
		}
	})

	t.Run("unique ids and metadata-less files pass", func(t *testing.T) {
		files := []pgmi.FileMetadata{
			{Path: "./a.sql", Metadata: &pgmi.ScriptMetadata{ID: id1}},
			{Path: "./b.sql", Metadata: &pgmi.ScriptMetadata{ID: id2}},
			{Path: "./c.sql", Metadata: nil},
			{Path: "./d.txt", Metadata: nil},
		}
		if err := validateNoDuplicateScriptIDs(files); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})
}

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

	_, err := sm.PrepareSession(context.TODO(), &pgmi.ConnectionConfig{}, "/src", nil, "", false)
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

	_, err := sm.PrepareSession(context.TODO(), &pgmi.ConnectionConfig{}, "/src", nil, "", false)
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

	_, err := sm.PrepareSession(context.Background(), &pgmi.ConnectionConfig{}, "/src", nil, "", false)
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

	_, err := sm.PrepareSession(context.Background(), &pgmi.ConnectionConfig{}, "/src", nil, "", false)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "database connection failed") {
		t.Errorf("Expected 'database connection failed', got: %v", err)
	}
}
