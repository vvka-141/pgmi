package scaffold_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

// A concurrent index build that fails leaves an INVALID index, which
// CREATE INDEX CONCURRENTLY IF NOT EXISTS then skips on every later deploy.
// The deploy tail fails on it, by name, until it is dropped.
func TestAdvancedTemplate_TailRefusesInvalidIndex(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, nil)

	const db = "pgmi_advanced_invalid_index"
	defer testhelpers.CleanupTestDB(t, connString, db)
	params := map[string]string{"env": "dev"}
	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	pool := testhelpers.GetTestPool(t, connString, db)
	if _, err := pool.Exec(ctx, "CREATE TABLE core.tail_probe (n int); INSERT INTO core.tail_probe VALUES (1), (1)"); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY tail_probe_n ON core.tail_probe (n)"); err == nil {
		t.Fatal("a unique index over duplicate rows was built")
	}

	err := deployAdvanced(t, connString, db, projectPath, false, params)
	if err == nil {
		t.Fatal("deploy succeeded with an invalid index in the database")
	}
	if !strings.Contains(err.Error(), "core.tail_probe_n") {
		t.Fatalf("want the error to name core.tail_probe_n, got: %v", err)
	}

	if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY core.tail_probe_n"); err != nil {
		t.Fatalf("drop the invalid index: %v", err)
	}
	if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
		t.Fatalf("deploy after dropping the invalid index: %v", err)
	}
}

// pg_get_viewdef omits a view's options, so a view altered by hand to drop
// security_invoker looked unchanged to core.ensure_view and kept running with
// its owner's rights. The next deploy restores it.
func TestAdvancedTemplate_RedeployRepairsViewOptions(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, nil)

	const db = "pgmi_advanced_view_options"
	defer testhelpers.CleanupTestDB(t, connString, db)
	params := map[string]string{"env": "dev"}
	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	views := []string{"api.vw_current_user", "membership.vw_user_principal"}
	pool := testhelpers.GetTestPool(t, connString, db)
	for _, v := range views {
		if _, err := pool.Exec(ctx, "ALTER VIEW "+v+" RESET (security_invoker)"); err != nil {
			t.Fatalf("reset security_invoker on %s: %v", v, err)
		}
	}

	if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
		t.Fatalf("redeploy: %v", err)
	}

	for _, v := range views {
		var invoker bool
		if err := pool.QueryRow(ctx,
			"SELECT coalesce('security_invoker=true' = ANY (reloptions), false) FROM pg_class WHERE oid = $1::regclass", v).Scan(&invoker); err != nil {
			t.Fatalf("read %s options: %v", v, err)
		}
		if !invoker {
			t.Errorf("%s lacks security_invoker after the redeploy", v)
		}
	}
}
