package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func writeProjectFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The deploy tool promises to map 1:1 to `pgmi deploy`. Each case configures
// one source the MCP handler used to ignore and resolves it both ways.
func TestServeDeployResolvesLikeCLI(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		files map[string]string
		env   map[string]string
		cli   func(dir string)
		mcp   func(dir string) map[string]any
		check func(t *testing.T, cfg deployJob)
	}{
		{
			name: "yaml params",
			yaml: "params:\n  region: eu\n",
			cli: func(string) {
				deployFlags.connection = "postgresql://u@db.example:6543/postgres"
				deployFlags.database = "app"
			},
			mcp: func(string) map[string]any {
				return map[string]any{"connection": "postgresql://u@db.example:6543/postgres", "database": "app"}
			},
			check: func(t *testing.T, cfg deployJob) {
				if cfg.Parameters["region"] != "eu" {
					t.Errorf("region = %q, want eu", cfg.Parameters["region"])
				}
			},
		},
		{
			name: "yaml connection and database",
			yaml: "connection:\n  host: db.example\n  port: 6543\n  username: u\n  database: fromyaml\n  sslmode: disable\n",
			cli:  func(string) {},
			mcp:  func(string) map[string]any { return map[string]any{} },
			check: func(t *testing.T, cfg deployJob) {
				if cfg.DatabaseName != "fromyaml" || !strings.Contains(cfg.ConnectionString, "db.example") {
					t.Errorf("yaml connection not applied: db=%q conn=%q", cfg.DatabaseName, cfg.ConnectionString)
				}
			},
		},
		{
			name: "yaml timeout",
			yaml: "timeout: 7s\n",
			cli: func(string) {
				deployFlags.connection = "postgresql://u@db.example/postgres"
				deployFlags.database = "app"
			},
			mcp: func(string) map[string]any {
				return map[string]any{"connection": "postgresql://u@db.example/postgres", "database": "app"}
			},
			check: func(t *testing.T, cfg deployJob) {
				if cfg.timeout != 7*time.Second {
					t.Errorf("timeout = %v, want 7s", cfg.timeout)
				}
			},
		},
		{
			name: "env connection",
			env:  map[string]string{"PGMI_CONNECTION_STRING": "postgresql://u@envhost:6543/envdb"},
			cli:  func(string) {},
			mcp:  func(string) map[string]any { return map[string]any{} },
			check: func(t *testing.T, cfg deployJob) {
				if cfg.DatabaseName != "envdb" || !strings.Contains(cfg.ConnectionString, "envhost") {
					t.Errorf("env connection not applied: db=%q conn=%q", cfg.DatabaseName, cfg.ConnectionString)
				}
			},
		},
		{
			name:  "params file",
			yaml:  "params:\n  region: eu\n",
			files: map[string]string{"a.env": "region=us\ntier=gold\n", "b.env": "Region=ap\n"},
			cli: func(dir string) {
				deployFlags.connection = "postgresql://u@db.example/postgres"
				deployFlags.database = "app"
				deployFlags.paramsFiles = []string{filepath.Join(dir, "a.env"), filepath.Join(dir, "b.env")}
				deployFlags.params = []string{"tier=platinum"}
			},
			mcp: func(dir string) map[string]any {
				return map[string]any{
					"connection": "postgresql://u@db.example/postgres", "database": "app",
					"paramsFile": []string{filepath.Join(dir, "a.env"), filepath.Join(dir, "b.env")},
					"params":     map[string]string{"tier": "platinum"},
				}
			},
			check: func(t *testing.T, cfg deployJob) {
				if cfg.Parameters["region"] != "ap" || cfg.Parameters["tier"] != "platinum" {
					t.Errorf("params = %v, want region=ap tier=platinum", cfg.Parameters)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearPGEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			dir := t.TempDir()
			if tt.yaml != "" {
				writeProjectFile(t, dir, "pgmi.yaml", tt.yaml)
			}
			for name, content := range tt.files {
				writeProjectFile(t, dir, name, content)
			}

			resetDeployFlags()
			t.Cleanup(resetDeployFlags)
			deployFlags.timeout = 3 * time.Minute
			deployFlags.jsonOutput = true
			tt.cli(dir)
			projectCfg, err := loadProjectConfig(dir)
			if err != nil {
				t.Fatal(err)
			}
			cliCfg, err := buildDeploymentConfig(deployCmd, dir, projectCfg, dotenvReport{}, false)
			if err != nil {
				t.Fatalf("CLI: %v", err)
			}

			args := tt.mcp(dir)
			args["path"] = dir
			raw, _ := json.Marshal(args)
			var a mcpDeployArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				t.Fatal(err)
			}
			mcpCfg, _, err := mcpDeploymentConfig(a)
			if err != nil {
				t.Fatalf("MCP: %v", err)
			}

			tt.check(t, mcpCfg)
			mcpCfg.Force = cliCfg.Force
			if !reflect.DeepEqual(cliCfg, mcpCfg) {
				t.Errorf("MCP resolved differently from the CLI:\n cli=%+v\n mcp=%+v", cliCfg, mcpCfg)
			}
		})
	}
}

func TestServeDeployParamsFileAcceptsString(t *testing.T) {
	var a mcpDeployArgs
	if err := json.Unmarshal([]byte(`{"paramsFile":"one.env"}`), &a); err != nil {
		t.Fatal(err)
	}
	if len(a.ParamsFile) != 1 || a.ParamsFile[0] != "one.env" {
		t.Errorf("paramsFile = %v", a.ParamsFile)
	}
	if err := json.Unmarshal([]byte(`{"paramsFile":7}`), &a); err == nil {
		t.Error("a number must be rejected")
	}
}

func TestServeDeployCaseVariantParamsAreInvalidConfig(t *testing.T) {
	clearPGEnv(t)
	_, _, err := mcpDeploymentConfig(mcpDeployArgs{
		Path:       t.TempDir(),
		Connection: "postgresql://u@db.example/postgres",
		Database:   "app",
		Params:     map[string]string{"Env": "a", "env": "b"},
	})
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
		t.Errorf("exit %d, want %d: %v", got, pgmi.ExitConfigError, err)
	}
}

func TestServeInitDefaultsNameToDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "orders_app")
	if result, isErr := callTool(t, "init", map[string]any{"path": dir}); isErr {
		t.Fatalf("init errored: %s", resultText(t, result))
	}
	data, err := os.ReadFile(filepath.Join(dir, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if p["app_name"] != "orders_app" {
		t.Errorf("app_name = %v, want orders_app", p["app_name"])
	}
}
