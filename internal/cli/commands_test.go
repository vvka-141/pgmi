package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestDeployCmd_ArgsValidation(t *testing.T) {
	err := deployCmd.Args(deployCmd, []string{})
	if err == nil {
		t.Fatal("Expected error for missing args")
	}
	exitCode := pgmi.ExitCodeForError(err)
	if exitCode != pgmi.ExitUsageError {
		t.Errorf("Expected exit code %d (usage), got %d for: %v", pgmi.ExitUsageError, exitCode, err)
	}
}

func TestDeployCmd_ArgsValidation_TooMany(t *testing.T) {
	err := deployCmd.Args(deployCmd, []string{"a", "b"})
	if err == nil {
		t.Fatal("Expected error for too many args")
	}
}

func TestDeployCmd_NonexistentPath(t *testing.T) {
	resetDeployFlags()
	deployFlags.connection = "postgresql://localhost/postgres"
	deployFlags.database = "testdb"

	err := runDeploy(deployCmd, []string{"/nonexistent/path/abc123"})
	if err == nil {
		t.Fatal("Expected error for nonexistent path")
	}
}

func TestDeployCmd_MissingDatabase(t *testing.T) {
	resetDeployFlags()
	tempDir := t.TempDir()
	deployFlags.connection = "postgresql://localhost/postgres"

	err := runDeploy(deployCmd, []string{tempDir})
	if err == nil {
		t.Fatal("Expected error for missing database")
	}
}

func TestDeployCmd_MissingConnectionInfo(t *testing.T) {
	resetDeployFlags()
	tempDir := t.TempDir()
	deployFlags.database = "testdb"

	for _, envVar := range []string{"PGMI_CONNECTION_STRING", "DATABASE_URL", "PGHOST"} {
		t.Setenv(envVar, "")
	}

	err := runDeploy(deployCmd, []string{tempDir})
	if err == nil {
		t.Fatal("Expected error for missing connection info")
	}
}

func TestDeployCmd_ForceWithoutOverwrite(t *testing.T) {
	resetDeployFlags()
	tempDir := t.TempDir()
	deployFlags.connection = "postgresql://localhost/postgres"
	deployFlags.database = "testdb"
	deployFlags.force = true
	deployFlags.overwrite = false

	err := runDeploy(deployCmd, []string{tempDir})
	if err == nil {
		t.Fatal("Expected error for force without overwrite")
	}
	if !strings.Contains(err.Error(), "force") || !strings.Contains(err.Error(), "overwrite") {
		t.Errorf("Expected error about force/overwrite, got: %v", err)
	}
}

func TestInitCmd_ArgsValidation(t *testing.T) {
	err := initCmd.Args(initCmd, []string{})
	if err == nil {
		t.Fatal("Expected error for missing args")
	}
}

func TestInitCmd_ArgsValidation_TooMany(t *testing.T) {
	err := initCmd.Args(initCmd, []string{"a", "b"})
	if err == nil {
		t.Fatal("Expected error for too many args")
	}
}

func TestDeployCmd_UnreachableHost_ExitCode11(t *testing.T) {
	resetDeployFlags()
	clearPGEnv(t)

	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "deploy.sql"), []byte("SELECT 1;"), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}

	deployFlags.host = "nonexistent.invalid"
	deployFlags.port = 5432
	deployFlags.database = "testdb"
	deployFlags.username = "testuser"
	deployFlags.timeout = 500 * time.Millisecond

	err := runDeploy(deployCmd, []string{tempDir})

	if err == nil {
		t.Fatal("Expected connection error, got nil")
	}

	exitCode := pgmi.ExitCodeForError(err)
	if exitCode != pgmi.ExitConnectionError {
		t.Errorf("Expected exit code %d (connection error), got %d for: %v",
			pgmi.ExitConnectionError, exitCode, err)
	}
}
