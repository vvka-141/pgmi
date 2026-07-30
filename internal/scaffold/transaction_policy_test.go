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

// TestTemplateTransactionPolicy exercises resolve-then-open (PGMI-180/154) end
// to end at REAL transaction characteristics — the part the SQL harness cannot
// cover, because its savepoint-wrapped steps run inside the deploy transaction
// and cannot reopen it. Here the driver plays the client gateway: it asks
// api.rest_route_policy for the route's characteristics, opens the transaction
// with them, and dispatches.
func TestTemplateTransactionPolicy(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	efs := filesystem.NewEmbedFileSystem(scaffold.GetTemplatesFS(), "templates/advanced")
	testDB := "pgmi_test_tx_policy"
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

	register := func(id, uri, name, meta, body string) {
		t.Helper()
		full := `{"id":"` + id + `","uri":"^` + uri + `$","httpMethod":"^GET$","name":"` + name +
			`","requiresAuth":false` + meta + `}`
		if _, err := pool.Exec(ctx, "SELECT api.create_or_replace_rest_handler($1::jsonb, $2)", full, body); err != nil {
			t.Fatalf("register handler %s: %v", name, err)
		}
	}
	okBody := "BEGIN\n RETURN api.json_response(200, jsonb_build_object('ok', true));\nEND;"
	register("eeeeeeee-7501-4000-8000-000000000001", "/ro-report", "ro_report",
		`,"readOnly":true,"minTransactionIsolation":"repeatable read"`, okBody)
	register("eeeeeeee-7502-4000-8000-000000000001", "/ro-ser", "ro_ser",
		`,"readOnly":true,"minTransactionIsolation":"serializable"`, okBody)

	// A read-only-declared handler that writes: the READ ONLY transaction must
	// turn the write into a hard error (25006), surfaced sanitized.
	if _, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS public.policy_probe(x int)"); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	register("eeeeeeee-7503-4000-8000-000000000001", "/ro-writer", "ro_writer",
		`,"readOnly":true`,
		"BEGIN\n INSERT INTO public.policy_probe VALUES (1);\n RETURN api.json_response(200, jsonb_build_object('ok', true));\nEND;")

	// The same write with NO read-only declaration. Dispatched inside a READ
	// ONLY transaction it raises the same 25006, but the remedy is the
	// caller's, so the gateway must answer 428 rather than 500.
	register("eeeeeeee-7504-4000-8000-000000000001", "/rw-writer", "rw_writer",
		``,
		"BEGIN\n INSERT INTO public.policy_probe VALUES (1);\n RETURN api.json_response(200, jsonb_build_object('ok', true));\nEND;")

	type policy struct {
		isolation  *string
		readOnly   bool
		deferrable bool
	}
	resolve := func(t *testing.T, method, url, requested string) policy {
		t.Helper()
		var p policy
		var req *string
		if requested != "" {
			req = &requested
		}
		err := pool.QueryRow(ctx,
			"SELECT transaction_isolation, transaction_read_only, transaction_deferrable FROM api.rest_route_policy($1, $2, $3)",
			method, url, req).Scan(&p.isolation, &p.readOnly, &p.deferrable)
		if err != nil {
			t.Fatalf("rest_route_policy %s %s: %v", method, url, err)
		}
		return p
	}

	txOptions := func(p policy) pgx.TxOptions {
		opts := pgx.TxOptions{}
		if p.isolation != nil {
			opts.IsoLevel = pgx.TxIsoLevel(*p.isolation)
		}
		if p.readOnly {
			opts.AccessMode = pgx.ReadOnly
		}
		if p.deferrable {
			opts.DeferrableMode = pgx.Deferrable
		}
		return opts
	}

	invokeWith := func(t *testing.T, opts pgx.TxOptions, url string) int {
		t.Helper()
		tx, err := pool.BeginTx(ctx, opts)
		if err != nil {
			t.Fatalf("begin tx %+v: %v", opts, err)
		}
		defer tx.Rollback(ctx)
		var status int
		err = tx.QueryRow(ctx,
			"SELECT (api.rest_invoke('GET', $1, ''::extensions.hstore, NULL::bytea)).status_code",
			url).Scan(&status)
		if err != nil {
			t.Fatalf("rest_invoke %s: %v", url, err)
		}
		return status
	}

	t.Run("no request resolves the floor and read-only", func(t *testing.T) {
		p := resolve(t, "GET", "/ro-report", "")
		if p.isolation == nil || *p.isolation != "repeatable read" || !p.readOnly || p.deferrable {
			t.Fatalf("expected (repeatable read, ro, not deferrable), got %+v", p)
		}
	})
	t.Run("weaker request cannot downgrade", func(t *testing.T) {
		p := resolve(t, "GET", "/ro-report", "read committed")
		if p.isolation == nil || *p.isolation != "repeatable read" {
			t.Fatalf("expected the floor to win, got %+v", p)
		}
	})
	t.Run("stronger request escalates and turns deferrable", func(t *testing.T) {
		p := resolve(t, "GET", "/ro-report", "serializable")
		if p.isolation == nil || *p.isolation != "serializable" || !p.deferrable {
			t.Fatalf("expected escalated SERIALIZABLE READ ONLY DEFERRABLE, got %+v", p)
		}
	})

	t.Run("resolved characteristics dispatch the route", func(t *testing.T) {
		p := resolve(t, "GET", "/ro-report", "")
		if got := invokeWith(t, txOptions(p), "/ro-report"); got != 200 {
			t.Fatalf("expected 200 opening with resolved policy, got %d", got)
		}
	})
	t.Run("serializable read-only deferrable dispatches", func(t *testing.T) {
		p := resolve(t, "GET", "/ro-ser", "")
		if p.isolation == nil || *p.isolation != "serializable" || !p.readOnly || !p.deferrable {
			t.Fatalf("expected SERIALIZABLE READ ONLY DEFERRABLE, got %+v", p)
		}
		if got := invokeWith(t, txOptions(p), "/ro-ser"); got != 200 {
			t.Fatalf("expected 200 at SERIALIZABLE READ ONLY DEFERRABLE, got %d", got)
		}
	})

	t.Run("read-write transaction is rejected fail-closed", func(t *testing.T) {
		got := invokeWith(t, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "/ro-report")
		if got != 428 {
			t.Fatalf("expected 428 for a read-only route in a read-write tx, got %d", got)
		}
	})

	// SQLSTATE 25006 reaches the gateway two ways and they are different
	// defects. A route that declared read_only and wrote anyway broke its own
	// contract, so the client is told nothing actionable: 500. A route that made
	// no such declaration, dispatched inside a READ ONLY transaction, is the
	// mirror of the 428 above -- the caller opened the wrong transaction and can
	// reopen it. Collapsing both to one status was the original defect;
	// collapsing both to 428 was the first attempt at fixing it, and this test
	// is what caught that.
	t.Run("read-write route in a read-only transaction asks the caller to reopen", func(t *testing.T) {
		got := invokeWith(t, pgx.TxOptions{AccessMode: pgx.ReadOnly}, "/rw-writer")
		if got != 428 {
			t.Fatalf("expected 428 for a read-write route in a READ ONLY tx, got %d", got)
		}
	})

	t.Run("write in a read-only handler is a hard error", func(t *testing.T) {
		p := resolve(t, "GET", "/ro-writer", "")
		if !p.readOnly {
			t.Fatalf("expected read-only policy, got %+v", p)
		}
		got := invokeWith(t, txOptions(p), "/ro-writer")
		if got != 500 {
			t.Fatalf("expected sanitized 500 when a read-only handler writes, got %d", got)
		}
		var rows int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.policy_probe").Scan(&rows); err != nil {
			t.Fatalf("count probe rows: %v", err)
		}
		if rows != 0 {
			t.Fatalf("READ ONLY transaction must prevent the write, found %d rows", rows)
		}
	})
}
