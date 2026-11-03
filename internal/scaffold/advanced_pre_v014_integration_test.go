package scaffold_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/internal/testhelpers"
)

// The deploy log as v0.13.1 created it: no pgmi_checksum column.
const v013DeployLog = `
CREATE SCHEMA internal;
CREATE TABLE internal.deployment_script_execution_log(
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deployment_script_object_id uuid NOT NULL,
    deployment_script_content_checksum TEXT NOT NULL,
    xact_id xid8 NOT NULL,
    file_path TEXT NOT NULL,
    idempotent BOOLEAN NOT NULL,
    sort_key TEXT,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    executed_by TEXT NOT NULL DEFAULT CURRENT_USER
);`

// A database built by v0.13 or earlier is refused before the deploy touches
// it, with the way forward, instead of failing midway on a missing column.
func TestAdvancedTemplate_RefusesPreV014Database(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := scaffoldAdvanced(t, nil)
	const db = "pgmi_advanced_pre_v014"
	testhelpers.CleanupTestDB(t, connString, db)
	t.Cleanup(testhelpers.CreateTestDB(t, connString, db))

	pool := testhelpers.GetTestPool(t, connString, db)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, v013DeployLog); err != nil {
		t.Fatalf("build the v0.13 deploy log: %v", err)
	}

	err := deployAdvanced(t, connString, db, projectPath, false, map[string]string{"env": "dev"})
	if err == nil {
		t.Fatal("deploy onto a v0.13 database succeeded; it must be refused")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55000" ||
		!strings.Contains(pgErr.Message, "built by pgmi v0.13 or earlier") ||
		!strings.Contains(pgErr.Hint, "docs/advanced/UPGRADING.md") {
		t.Fatalf("want the pre-v0.14 refusal (55000) with a hint to UPGRADING.md, got: %v", err)
	}

	var schemas, relations, roles int
	if qerr := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM pg_namespace WHERE nspname IN ('core', 'api', 'membership', 'common', 'extensions')),
		       (SELECT count(*) FROM pg_class WHERE relnamespace = 'internal'::regnamespace),
		       (SELECT count(*) FROM pg_roles WHERE rolname LIKE $1 || '\_%')`, db).Scan(&schemas, &relations, &roles); qerr != nil {
		t.Fatalf("inspect the refused database: %v", qerr)
	}
	if schemas != 0 || relations != 3 || roles != 0 {
		t.Errorf("the refused deploy changed the database: %d schemas, %d relations in internal (want 3), %d roles", schemas, relations, roles)
	}
}
