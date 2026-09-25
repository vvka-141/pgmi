package contract_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/contract"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/internal/testhelpers"
)

func sessionConn(t *testing.T) *pgxpool.Conn {
	t.Helper()
	connStr := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(conn.Release)

	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if _, err := contract.Apply(ctx, conn, ""); err != nil {
		t.Fatalf("contract.Apply: %v", err)
	}
	return conn
}

// The callback is spliced into generated SQL with %s, because a qualified
// name cannot be %I-quoted as one identifier. The regex guard is the only
// thing between a caller-chosen string and SQL run with the deployer's
// privileges, so it is exercised here rather than grepped for.
func TestPgmiTestGenerate_RejectsNonIdentifierCallback(t *testing.T) {
	conn := sessionConn(t)
	ctx := context.Background()

	if _, err := conn.Exec(ctx, `
		INSERT INTO pg_temp._pgmi_test_directory (path, parent_path, depth) VALUES ('./__test__/', NULL, 0);
		INSERT INTO pg_temp._pgmi_test_source (path, directory, filename, content, is_fixture)
		VALUES ('./__test__/test_a.sql', './__test__/', 'test_a.sql', 'SELECT 1', false);`); err != nil {
		t.Fatalf("seed a test file: %v", err)
	}

	for _, callback := range []string{
		"x; DROP TABLE t; --",
		`"weird name"`,
		"a.b.c",
		"1abc",
		"f(1)",
	} {
		t.Run(callback, func(t *testing.T) {
			var sql string
			err := conn.QueryRow(ctx, `SELECT pg_temp.pgmi_test_generate(NULL, $1)`, callback).Scan(&sql)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "22023" {
				t.Fatalf("callback %q: want SQLSTATE 22023, got err=%v sql=%q", callback, err, sql)
			}
		})
	}

	var sql string
	if err := conn.QueryRow(ctx, `SELECT pg_temp.pgmi_test_generate(NULL, 'pg_temp.my_callback')`).Scan(&sql); err != nil {
		t.Fatalf("a qualified identifier must be accepted: %v", err)
	}
	if !strings.Contains(sql, "pg_temp.my_callback(") {
		t.Errorf("generated SQL does not call the accepted callback:\n%s", sql)
	}
}

// pgmi_persist_test_plan is the one session function that writes outside
// pg_temp, into a caller-named schema. It must %I-quote that name.
func TestPersistTestPlan_QuotesTargetSchema(t *testing.T) {
	conn := sessionConn(t)
	ctx := context.Background()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `CREATE SCHEMA "weird schema"; CREATE TABLE public.pgmi_canary (id int)`); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_temp.pgmi_persist_test_plan('weird schema')`); err != nil {
		t.Fatalf("a schema name needing quotes must work: %v", err)
	}
	var landed bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('"weird schema".pgmi_test_plan') IS NOT NULL`).Scan(&landed); err != nil || !landed {
		t.Fatalf("plan table not created in \"weird schema\" (err=%v)", err)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT hostile`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `SELECT pg_temp.pgmi_persist_test_plan('public.pgmi_x (a int); DROP TABLE public.pgmi_canary; CREATE TABLE public')`)
	if err == nil {
		t.Fatal("a hostile schema name must fail as a nonexistent schema, not succeed")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT hostile`); err != nil {
		t.Fatal(err)
	}
	var canary bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.pgmi_canary') IS NOT NULL`).Scan(&canary); err != nil || !canary {
		t.Fatalf("the canary table is gone: the schema name was executed, not quoted (err=%v)", err)
	}
}
