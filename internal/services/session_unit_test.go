package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestNewSessionManager_NilDeps(t *testing.T) {
	connFactory := func(_ *db.ConnectionConfig) (db.Connector, error) {
		return &mockConnector{}, nil
	}

	tests := []struct {
		name string
		fn   func()
	}{
		{"nil connectorFactory", func() {
			NewSessionManager(nil, &mockFileScanner{}, &mockFileLoader{}, &mockLogger{}, "test")
		}},
		{"nil fileScanner", func() {
			NewSessionManager(connFactory, nil, &mockFileLoader{}, &mockLogger{}, "test")
		}},
		{"nil fileLoader", func() {
			NewSessionManager(connFactory, &mockFileScanner{}, nil, &mockLogger{}, "test")
		}},
		{"nil logger", func() {
			NewSessionManager(connFactory, &mockFileScanner{}, &mockFileLoader{}, nil, "test")
		}},
		{"empty pgmiVersion", func() {
			NewSessionManager(connFactory, &mockFileScanner{}, &mockFileLoader{}, &mockLogger{}, "")
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

// Content that can never load must be rejected while scanning, before Deploy
// creates or drops a database (PGMI-372).
func TestScanProject_UnloadableContentIsInvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		deploySQL []byte
		extra     map[string][]byte
		want      string
	}{
		{"deploy.sql not UTF-8", []byte("SELECT 1; -- \xff\xff"), nil, "deploy.sql is not valid UTF-8"},
		{"NUL byte in a project file", []byte("SELECT 1;"), map[string][]byte{"x.pyc": {0x42, 0x00, 0x0d}}, "contains a NUL byte"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), tt.deploySQL, 0o644); err != nil {
				t.Fatal(err)
			}
			for name, content := range tt.extra {
				if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			connFactory := func(_ *db.ConnectionConfig) (db.Connector, error) {
				t.Fatal("scanning must not create a connector")
				return nil, nil
			}
			sm := NewSessionManager(connFactory, scanner.NewScanner(checksum.New()), &mockFileLoader{}, &mockLogger{}, "test")

			_, err := sm.ScanProject(dir)
			if !errors.Is(err, pgmi.ErrInvalidConfig) {
				t.Fatalf("want ErrInvalidConfig (exit 10), got %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestScanProject_ValidateDeploySQLFails(t *testing.T) {
	connFactory := func(_ *db.ConnectionConfig) (db.Connector, error) {
		return &mockConnector{}, nil
	}
	fileScanner := &mockFileScanner{validateErr: fmt.Errorf("deploy.sql missing")}
	sm := NewSessionManager(connFactory, fileScanner, &mockFileLoader{}, &mockLogger{}, "test")

	_, err := sm.ScanProject("/src")
	if err == nil {
		t.Fatal("Expected error")
	}
	if err.Error() != "deploy.sql missing" {
		t.Errorf("the validator's own message must reach the user unwrapped, got: %v", err)
	}
}

func TestScanProject_ScanDirectoryFails(t *testing.T) {
	connFactory := func(_ *db.ConnectionConfig) (db.Connector, error) {
		return &mockConnector{}, nil
	}
	fileScanner := &mockFileScanner{scanErr: fmt.Errorf("permission denied")}
	sm := NewSessionManager(connFactory, fileScanner, &mockFileLoader{}, &mockLogger{}, "test")

	_, err := sm.ScanProject("/src")
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "failed to scan directory") {
		t.Errorf("Expected 'failed to scan directory' in error, got: %v", err)
	}
}

func TestPrepareSession_ConnectorFactoryFails(t *testing.T) {
	connFactory := func(_ *db.ConnectionConfig) (db.Connector, error) {
		return nil, fmt.Errorf("factory error")
	}
	fileScanner := &mockFileScanner{}
	sm := NewSessionManager(connFactory, fileScanner, &mockFileLoader{}, &mockLogger{}, "test")

	_, err := sm.PrepareSession(context.Background(), &db.ConnectionConfig{}, scanner.Result{}, nil, "", false)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "database connection failed") {
		t.Errorf("Expected 'database connection failed', got: %v", err)
	}
}

func TestPrepareSession_ConnectFails(t *testing.T) {
	connFactory := func(_ *db.ConnectionConfig) (db.Connector, error) {
		return &mockConnector{err: fmt.Errorf("connection refused")}, nil
	}
	fileScanner := &mockFileScanner{}
	sm := NewSessionManager(connFactory, fileScanner, &mockFileLoader{}, &mockLogger{}, "test")

	_, err := sm.PrepareSession(context.Background(), &db.ConnectionConfig{}, scanner.Result{}, nil, "", false)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "database connection failed") {
		t.Errorf("Expected 'database connection failed', got: %v", err)
	}
}
