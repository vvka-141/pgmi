package cli

import (
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestResolveDeploymentConfig_ConnectionStringWithoutDatabase(t *testing.T) {
	clearPGEnv(t)
	req := deployRequest{conn: connectionFlags{connection: "postgresql://u@127.0.0.1:5465"}}

	_, err := resolveDeploymentConfig(t.TempDir(), &config.ProjectConfig{}, req)
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
		t.Fatalf("no database anywhere: exit %d, want %d (%v)", got, pgmi.ExitConfigError, err)
	}

	cfg, err := resolveDeploymentConfig(t.TempDir(),
		&config.ProjectConfig{Connection: config.ConnectionConfig{Database: "myapp"}}, req)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseName != "myapp" || cfg.MaintenanceDatabase != "postgres" {
		t.Errorf("target %q, maintenance %q; want myapp from pgmi.yaml, postgres for create/drop",
			cfg.DatabaseName, cfg.MaintenanceDatabase)
	}

	t.Setenv("PGDATABASE", "fromenv")
	cfg, err = resolveDeploymentConfig(t.TempDir(),
		&config.ProjectConfig{Connection: config.ConnectionConfig{Database: "myapp"}}, req)
	if err != nil || cfg.DatabaseName != "fromenv" {
		t.Errorf("PGDATABASE outranks pgmi.yaml: got %q, %v", cfg.DatabaseName, err)
	}
}

func TestResolveDeploymentConfig_NamesTLSOverrides(t *testing.T) {
	yaml := &config.ProjectConfig{Connection: config.ConnectionConfig{
		Host: "127.0.0.1", Database: "myapp", SSLMode: "verify-full"}}

	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"PGSSLMODE over pgmi.yaml", map[string]string{"PGSSLMODE": "disable"},
			"sslmode: from PGSSLMODE (disable), not pgmi.yaml (verify-full)"},
		{"an env connection string drops pgmi.yaml's sslmode",
			map[string]string{"DATABASE_URL": "postgresql://u@127.0.0.1/postgres"},
			"sslmode: pgmi.yaml (verify-full) does not apply to the connection string from DATABASE_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearPGEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			stderr := captureStderr(t, func() {
				if _, err := resolveDeploymentConfig(t.TempDir(), yaml, deployRequest{}); err != nil {
					t.Fatal(err)
				}
			})
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr does not name the override %q:\n%s", tc.want, stderr)
			}
		})
	}
}

// pgx applies PGOPTIONS to every session; a deploy names it so a changed
// setting is never a mystery.
func TestResolveDeploymentConfig_NamesPGOPTIONS(t *testing.T) {
	clearPGEnv(t)
	t.Setenv("PGOPTIONS", "-c default_transaction_read_only=on")
	req := deployRequest{conn: connectionFlags{connection: "postgresql://u@127.0.0.1/postgres"}}
	stderr := captureStderr(t, func() {
		if _, err := resolveDeploymentConfig(t.TempDir(), &config.ProjectConfig{Connection: config.ConnectionConfig{Database: "myapp"}}, req); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "session options: from PGOPTIONS") || strings.Contains(stderr, "read_only") {
		t.Errorf("want the variable named and its value not printed, got:\n%s", stderr)
	}
}
