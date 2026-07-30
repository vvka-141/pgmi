package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/contract"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// The exit-code table in docs/CLI.md is the CI and agent interface. Every row
// documented as a configuration error used to arrive as exit 1 or exit 13, so
// an agent that forgot --param was sent to the exit-13 table to hunt a SQL bug
// that did not exist.
//
// These cases drive the real error paths rather than hand-built error values:
// the defect was never in the classifier, it was that the errors reaching it
// carried no sentinel.
func TestExitCode_ConfigurationErrors(t *testing.T) {
	clearPGEnv(t)

	sourcePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourcePath, "deploy.sql"), []byte("SELECT 1;"), 0o600); err != nil {
		t.Fatalf("write deploy.sql: %v", err)
	}

	tests := []struct {
		name       string
		setupFlags func()
		sourcePath string
	}{
		{
			name: "no database name anywhere",
			setupFlags: func() {
				deployFlags.host = "localhost"
				deployFlags.port = 5432
				deployFlags.username = "postgres"
				deployFlags.timeout = 3 * time.Minute
			},
			sourcePath: sourcePath,
		},
		{
			name: "malformed --param without an equals sign",
			setupFlags: func() {
				deployFlags.database = "myapp"
				deployFlags.host = "localhost"
				deployFlags.port = 5432
				deployFlags.username = "postgres"
				deployFlags.params = []string{"novalue"}
				deployFlags.timeout = 3 * time.Minute
			},
			sourcePath: sourcePath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetDeployFlags()
			tt.setupFlags()
			t.Cleanup(resetDeployFlags)

			_, err := buildDeploymentConfig(deployCmd, tt.sourcePath, nil, false)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
				t.Errorf("exit code %d, want %d (documented as a configuration error): %v",
					got, pgmi.ExitConfigError, err)
			}
		})
	}
}

// pgmi.yaml is read before the deployment config is assembled, so its parse
// failure is classified on its own path.
func TestExitCode_UnparseablePgmiYaml(t *testing.T) {
	sourcePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourcePath, "pgmi.yaml"), []byte("connection:\n  host: [unclosed\n"), 0o600); err != nil {
		t.Fatalf("write pgmi.yaml: %v", err)
	}

	_, err := loadProjectConfig(sourcePath)
	if err == nil {
		t.Fatal("expected an error for an unparseable pgmi.yaml")
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
		t.Errorf("exit code %d, want %d: %v", got, pgmi.ExitConfigError, err)
	}
}

// --compat resolves entirely offline, so it must be rejected as a configuration
// error rather than falling through to the generic exit 1.
func TestExitCode_UnsupportedCompatVersion(t *testing.T) {
	_, _, err := contract.Load("99")
	if err == nil {
		t.Fatal("expected an error for an unsupported API version")
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
		t.Errorf("exit code %d, want %d: %v", got, pgmi.ExitConfigError, err)
	}
}

// Unknown commands are wrapped with ErrUsage by Execute(), not sniffed in
// ExitCodeForError. This test drives the real command tree through Execute().
func TestExitCode_UnknownCommandIsUsageError(t *testing.T) {
	_, err := withRootArgs(t, "frobnicate")
	if err == nil {
		t.Fatal("expected an error for an unknown command")
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitUsageError {
		t.Errorf("exit code %d, want %d: %v", got, pgmi.ExitUsageError, err)
	}
}
