package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/scanner"
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
			connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
				t.Fatal("scanning must not create a connector")
				return nil, nil
			}
			sm := NewSessionManager(connFactory, scanner.NewScanner(checksum.New()), &mockFileLoader{}, &mockLogger{})

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
	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return &mockConnector{}, nil
	}
	scanner := &mockFileScanner{validateErr: fmt.Errorf("deploy.sql missing")}
	sm := NewSessionManager(connFactory, scanner, &mockFileLoader{}, &mockLogger{})

	_, err := sm.ScanProject("/src")
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "failed to validate deploy.sql") {
		t.Errorf("Expected 'failed to validate deploy.sql' in error, got: %v", err)
	}
}

func TestScanProject_ScanDirectoryFails(t *testing.T) {
	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return &mockConnector{}, nil
	}
	scanner := &mockFileScanner{scanErr: fmt.Errorf("permission denied")}
	sm := NewSessionManager(connFactory, scanner, &mockFileLoader{}, &mockLogger{})

	_, err := sm.ScanProject("/src")
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "failed to scan directory") {
		t.Errorf("Expected 'failed to scan directory' in error, got: %v", err)
	}
}

func TestPrepareSession_ConnectorFactoryFails(t *testing.T) {
	connFactory := func(_ *pgmi.ConnectionConfig) (pgmi.Connector, error) {
		return nil, fmt.Errorf("factory error")
	}
	scanner := &mockFileScanner{}
	sm := NewSessionManager(connFactory, scanner, &mockFileLoader{}, &mockLogger{})

	_, err := sm.PrepareSession(context.Background(), &pgmi.ConnectionConfig{}, pgmi.FileScanResult{}, nil, "", false)
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

	_, err := sm.PrepareSession(context.Background(), &pgmi.ConnectionConfig{}, pgmi.FileScanResult{}, nil, "", false)
	if err == nil {
		t.Fatal("Expected error")
	}
	if !strings.Contains(err.Error(), "database connection failed") {
		t.Errorf("Expected 'database connection failed', got: %v", err)
	}
}
