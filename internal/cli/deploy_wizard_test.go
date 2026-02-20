package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNeedsConnectionWizard(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		want    bool
	}{
		{
			name:  "no config at all triggers wizard",
			setup: func(t *testing.T, dir string) {},
			want:  true,
		},
		{
			name: "connection flag suppresses wizard",
			setup: func(t *testing.T, dir string) {
				deployFlags.connection = "postgresql://localhost/db"
			},
			want: false,
		},
		{
			name: "host flag suppresses wizard",
			setup: func(t *testing.T, dir string) {
				deployFlags.host = "localhost"
			},
			want: false,
		},
		{
			name: "database flag suppresses wizard",
			setup: func(t *testing.T, dir string) {
				deployFlags.database = "mydb"
			},
			want: false,
		},
		{
			name: "DATABASE_URL env var suppresses wizard",
			setup: func(t *testing.T, dir string) {
				t.Setenv("DATABASE_URL", "postgresql://localhost/db")
			},
			want: false,
		},
		{
			name: "PGMI_CONNECTION_STRING env var suppresses wizard",
			setup: func(t *testing.T, dir string) {
				t.Setenv("PGMI_CONNECTION_STRING", "postgresql://localhost/db")
			},
			want: false,
		},
		{
			name: "PGHOST+PGDATABASE env vars suppress wizard",
			setup: func(t *testing.T, dir string) {
				t.Setenv("PGHOST", "localhost")
				t.Setenv("PGDATABASE", "mydb")
			},
			want: false,
		},
		{
			name: "PGHOST alone does not suppress wizard",
			setup: func(t *testing.T, dir string) {
				t.Setenv("PGHOST", "localhost")
			},
			want: true,
		},
		{
			name: "pgmi.yaml with host suppresses wizard",
			setup: func(t *testing.T, dir string) {
				yaml := "connection:\n  host: localhost\n"
				os.WriteFile(filepath.Join(dir, "pgmi.yaml"), []byte(yaml), 0644)
			},
			want: false,
		},
		{
			name: "pgmi.yaml with database suppresses wizard",
			setup: func(t *testing.T, dir string) {
				yaml := "connection:\n  database: mydb\n"
				os.WriteFile(filepath.Join(dir, "pgmi.yaml"), []byte(yaml), 0644)
			},
			want: false,
		},
		{
			name: "empty pgmi.yaml still triggers wizard",
			setup: func(t *testing.T, dir string) {
				os.WriteFile(filepath.Join(dir, "pgmi.yaml"), []byte(""), 0644)
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetDeployFlags()
			clearPGEnv(t)

			dir := t.TempDir()
			tt.setup(t, dir)

			projectCfg, _ := loadProjectConfig(dir)
			got := needsConnectionWizard(projectCfg)
			if got != tt.want {
				t.Errorf("needsConnectionWizard() = %v, want %v", got, tt.want)
			}
		})
	}
}
