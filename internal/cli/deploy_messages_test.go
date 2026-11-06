package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// What a failed deploy prints is the user's only lead. Each message names
// what broke, once, and nothing it did not touch.
func TestDeployCmd_ErrorMessages(t *testing.T) {
	run := func(t *testing.T, dir string, set func()) string {
		t.Helper()
		resetDeployFlags()
		clearPGEnv(t)
		deployFlags.force = true
		deployFlags.timeout = 30 * time.Second
		set()
		err := runDeploy(deployCmd, []string{dir})
		if err == nil {
			t.Fatal("the deploy succeeded")
		}
		return pgmi.FormatError(err)
	}
	expect := func(t *testing.T, out string, want []string, not []string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("message lacks %q:\n%s", w, out)
			}
		}
		for _, n := range not {
			if strings.Contains(out, n) {
				t.Errorf("message must not contain %q:\n%s", n, out)
			}
		}
	}

	t.Run("a missing project path", func(t *testing.T) {
		out := run(t, "nosuch", func() { deployFlags.connection = "postgresql://u@127.0.0.1:1/postgres" })
		expect(t, out, []string{`project path "nosuch" does not exist`},
			[]string{"file scanning failed", "failed to validate", ": invalid configuration"})
	})

	t.Run("--connection with -h", func(t *testing.T) {
		out := run(t, deployProjectDir(t), func() {
			deployFlags.connection = "postgresql://u@127.0.0.1:1/postgres"
			deployFlags.host = "other"
		})
		expect(t, out, []string{"cannot combine --connection with -h"}, []string{": invalid configuration"})
	})

	t.Run("--timeout at connect", func(t *testing.T) {
		out := run(t, deployProjectDir(t), func() {
			deployFlags.connection = "postgresql://u@10.255.255.1:5432/postgres"
			deployFlags.database = "app"
			deployFlags.timeout = 50 * time.Millisecond
		})
		expect(t, out, []string{"--timeout 50ms"}, []string{"maintenance database"})
	})

	connString := testhelpers.RequireDatabase(t)

	t.Run("--timeout at execute", func(t *testing.T) {
		const db = "pgmi_cli_msg_timeout"
		defer testhelpers.CleanupTestDB(t, connString, db)
		dir := t.TempDir()
		writeProjectFile(t, dir, "deploy.sql", "SELECT pg_sleep(5);\n")
		out := run(t, dir, func() {
			deployFlags.connection = connString
			deployFlags.database = db
			deployFlags.timeout = time.Second
		})
		expect(t, out, []string{"--timeout 1s", "--timeout 0"}, nil)
	})

	t.Run("a wrong password on the target", func(t *testing.T) {
		conn, err := db.ParseConnectionString(connString)
		if err != nil {
			t.Fatal(err)
		}
		out := run(t, deployProjectDir(t), func() {
			deployFlags.connection = strings.Replace(connString, ":"+conn.Password+"@", ":wrong-password@", 1)
			deployFlags.database = "pgmi_cli_msg_target"
		})
		expect(t, out, []string{`"pgmi_cli_msg_target"`, "password authentication failed"},
			[]string{"maintenance database", "failed to ensure database exists"})
	})
}
