package cli

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

const parityDeploySQL = `DO $$ BEGIN
    RAISE NOTICE 'parity region=% tier=% db=%',
        current_setting('pgmi.region', true), current_setting('pgmi.tier', true), current_database();
END $$;
SELECT pg_sleep(coalesce(current_setting('pgmi.sleep', true), '0')::float);
`

// The same project deployed through `pgmi deploy` and through the serve deploy
// tool must reach the same database with the same parameters and exit code.
func TestServeDeployMatchesCLI(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	conn, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatal(err)
	}
	const testDB = "pgmi_serve_parity"

	yamlConnection := fmt.Sprintf("connection:\n  host: %s\n  port: %d\n  username: %s\n  database: %s\n  sslmode: %s\n",
		conn.Host, conn.Port, conn.Username, testDB, cmp.Or(conn.SSLMode, "disable"))

	tests := []struct {
		name  string
		yaml  string
		files map[string]string
		env   map[string]string
		cli   func(dir string)
		mcp   func(dir string) map[string]any
	}{
		{
			name: "yaml params",
			yaml: "params:\n  region: eu\n",
			cli:  func(string) { deployFlags.connection = connString; deployFlags.database = testDB },
			mcp:  func(string) map[string]any { return map[string]any{"connection": connString, "database": testDB} },
		},
		{
			name: "yaml connection and database",
			yaml: yamlConnection + "params:\n  region: eu\n",
			env:  map[string]string{"PGPASSWORD": conn.Password},
			cli:  func(string) {},
			mcp:  func(string) map[string]any { return map[string]any{} },
		},
		{
			name: "yaml timeout",
			yaml: "timeout: 1s\nparams:\n  sleep: \"5\"\n",
			cli:  func(string) { deployFlags.connection = connString; deployFlags.database = testDB },
			mcp:  func(string) map[string]any { return map[string]any{"connection": connString, "database": testDB} },
		},
		{
			name: "env connection",
			env:  map[string]string{"PGMI_CONNECTION_STRING": connString},
			cli:  func(string) { deployFlags.database = testDB },
			mcp:  func(string) map[string]any { return map[string]any{"database": testDB} },
		},
		{
			name:  "params file",
			yaml:  "params:\n  region: eu\n",
			files: map[string]string{"a.env": "region=us\ntier=gold\n", "b.env": "Region=ap\n"},
			cli: func(dir string) {
				deployFlags.connection = connString
				deployFlags.database = testDB
				deployFlags.paramsFiles = []string{filepath.Join(dir, "a.env"), filepath.Join(dir, "b.env")}
			},
			mcp: func(dir string) map[string]any {
				return map[string]any{"connection": connString, "database": testDB,
					"paramsFile": []string{filepath.Join(dir, "a.env"), filepath.Join(dir, "b.env")}}
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
			writeProjectFile(t, dir, "deploy.sql", parityDeploySQL)
			if tt.yaml != "" {
				writeProjectFile(t, dir, "pgmi.yaml", tt.yaml)
			}
			for name, content := range tt.files {
				writeProjectFile(t, dir, name, content)
			}

			testhelpers.CleanupTestDB(t, connString, testDB)
			resetDeployFlags()
			t.Cleanup(resetDeployFlags)
			deployFlags.timeout = 3 * time.Minute
			deployFlags.force = true
			tt.cli(dir)
			cliNotices := captureNotices(t)
			cliErr := runDeploy(deployCmd, []string{dir})
			cliExit := pgmi.ExitCodeForError(cliErr)
			cliParity := cliNotices()

			testhelpers.CleanupTestDB(t, connString, testDB)
			args := tt.mcp(dir)
			args["path"] = dir
			result, _ := callTool(t, "deploy", args)
			sc, _ := result["structuredContent"].(map[string]any)
			mcpExit, _ := sc["exitCode"].(float64)
			var mcpParity []string
			notices, _ := sc["notices"].([]any)
			for _, n := range notices {
				msg, _ := n.(map[string]any)["message"].(string)
				if strings.HasPrefix(msg, "parity ") {
					mcpParity = append(mcpParity, msg)
				}
			}
			testhelpers.CleanupTestDB(t, connString, testDB)

			if cliExit != int(mcpExit) {
				t.Errorf("exit: cli=%d mcp=%v (cli err: %v; mcp: %v)", cliExit, mcpExit, cliErr, sc)
			}
			if !slices.Equal(cliParity, mcpParity) {
				t.Errorf("notices differ:\n cli=%q\n mcp=%q", cliParity, mcpParity)
			}
			if tt.name != "yaml timeout" && len(cliParity) != 1 {
				t.Errorf("expected one parity notice, got %q (cli err: %v)", cliParity, cliErr)
			}
		})
	}
}

func captureNotices(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	orig := db.NoticeHandler
	db.NoticeHandler = func(_, message, _, _ string) {
		if strings.HasPrefix(message, "parity ") {
			mu.Lock()
			got = append(got, message)
			mu.Unlock()
		}
	}
	t.Cleanup(func() { db.NoticeHandler = orig })
	return func() []string {
		db.NoticeHandler = orig
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func TestServeDeployAnswersAShortTextAndTheFullRecord(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const testDB = "pgmi_serve_summary"
	dir := t.TempDir()
	writeProjectFile(t, dir, "deploy.sql", `DO $$ BEGIN
    FOR i IN 1..300 LOOP RAISE NOTICE 'line % of a long deploy', i; END LOOP;
    RAISE DEBUG 'serve-debug-marker';
END $$;
`)
	writeProjectFile(t, dir, ".pgmiignore", "scratch/\n")
	if err := os.MkdirAll(filepath.Join(dir, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, dir, "scratch/notes.sql", "SELECT 1;")
	testhelpers.CleanupTestDB(t, connString, testDB)
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, testDB) })

	for _, verbose := range []bool{false, true} {
		result, isErr := callTool(t, "deploy", map[string]any{
			"path": dir, "connection": connString, "database": testDB, "verbose": verbose})
		if isErr {
			t.Fatalf("deploy failed: %v", result)
		}
		text := resultText(t, result)
		if len(text) > 500 || !strings.Contains(text, testDB) {
			t.Errorf("success text must be a short summary naming the database, got %d bytes: %q", len(text), text)
		}
		sc, _ := result["structuredContent"].(map[string]any)
		ignored, _ := sc["ignoredPaths"].([]any)
		if !slices.ContainsFunc(ignored, func(e any) bool {
			m, _ := e.(map[string]any)
			return m["path"] == "./scratch/" && m["rule"] == pgmi.IgnoreRulePgmiignore
		}) || float64(len(ignored)) != sc["pathsIgnored"] {
			t.Errorf("ignoredPaths = %v, pathsIgnored = %v", sc["ignoredPaths"], sc["pathsIgnored"])
		}
		sawDebug := false
		for _, n := range sc["notices"].([]any) {
			if n.(map[string]any)["message"] == "serve-debug-marker" {
				sawDebug = true
			}
		}
		if sawDebug != verbose {
			t.Errorf("verbose=%v: RAISE DEBUG reached the caller = %v", verbose, sawDebug)
		}
	}
}
