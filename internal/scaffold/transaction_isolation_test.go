package scaffold_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/scaffold"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestTemplateTransactionIsolation exercises the advanced template's isolation
// floor (PGMI-103/110/111) end to end at REAL transaction isolation levels —
// the part the SQL test harness cannot cover, because its savepoint-wrapped
// steps run at the deploy transaction's level (read committed) and
// SET TRANSACTION ISOLATION LEVEL is illegal mid-transaction. Here a driver
// opens each transaction at a chosen level via BEGIN ISOLATION LEVEL, so the
// "accepts an equal-or-stronger level" path is proven, not just the reject path.
func TestTemplateTransactionIsolation(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	efs := filesystem.NewEmbedFileSystem(scaffold.GetTemplatesFS(), "templates/advanced")
	testDB := "pgmi_test_tx_isolation"
	deployer := testhelpers.NewTestDeployerWithFS(t, efs)

	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString: connString,
		DatabaseName:     testDB,
		SourcePath:       ".",
		Overwrite:        true,
		Force:            true,
		Parameters: map[string]string{
			"database_admin_password":    "TestPassword123!",
			"database_customer_password": "CustomerPassword123!",
			"env":                        "test",
		},
		Verbose: testing.Verbose(),
	})
	if err != nil {
		t.Fatalf("advanced template deploy failed: %v", err)
	}
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	pool := testhelpers.GetTestPool(t, connString, testDB)

	// Register committed public REST handlers with isolation floors. (The SQL
	// __test__ handlers roll back after each step, so a driver test that opens
	// its own transactions needs handlers that persist.)
	body := "BEGIN\n RETURN api.json_response(200, jsonb_build_object('ok', true));\nEND;"
	register := func(id, uri, name, floor string) {
		t.Helper()
		meta := `{"id":"` + id + `","uri":"^` + uri + `$","httpMethod":"^GET$","name":"` + name +
			`","requiresAuth":false,"minTransactionIsolation":"` + floor + `"}`
		if _, err := pool.Exec(ctx, "SELECT api.create_or_replace_rest_handler($1::jsonb, $2)", meta, body); err != nil {
			t.Fatalf("register handler %s: %v", name, err)
		}
	}
	register("eeeeeeee-7401-4000-8000-000000000001", "/iso-ser", "iso_ser", "serializable")
	register("eeeeeeee-7402-4000-8000-000000000001", "/iso-rr", "iso_rr", "repeatable read")

	invokeAt := func(t *testing.T, iso pgx.TxIsoLevel, url string) int {
		t.Helper()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: iso})
		if err != nil {
			t.Fatalf("begin tx at %s: %v", iso, err)
		}
		defer tx.Rollback(ctx)
		var status int
		err = tx.QueryRow(ctx,
			"SELECT (api.rest_invoke('GET', $1, ''::extensions.hstore, NULL::bytea)).status_code",
			url).Scan(&status)
		if err != nil {
			t.Fatalf("rest_invoke %s at %s: %v", url, iso, err)
		}
		return status
	}

	cases := []struct {
		name string
		iso  pgx.TxIsoLevel
		url  string
		want int
	}{
		// serializable floor
		{"serializable floor rejects read committed", pgx.ReadCommitted, "/iso-ser", 428},
		{"serializable floor rejects repeatable read", pgx.RepeatableRead, "/iso-ser", 428},
		{"serializable floor accepts serializable", pgx.Serializable, "/iso-ser", 200},
		// repeatable read floor
		{"repeatable read floor rejects read committed", pgx.ReadCommitted, "/iso-rr", 428},
		{"repeatable read floor accepts repeatable read", pgx.RepeatableRead, "/iso-rr", 200},
		{"repeatable read floor accepts serializable", pgx.Serializable, "/iso-rr", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := invokeAt(t, c.iso, c.url); got != c.want {
				t.Fatalf("%s: expected status %d, got %d", c.name, c.want, got)
			}
		})
	}
}
