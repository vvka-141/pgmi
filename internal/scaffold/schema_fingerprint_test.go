package scaffold_test

import (
	"context"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
)

// fingerprintQuery summarises every catalog object a deploy could create in the
// project's own schemas — relations, their columns, constraints, policies,
// triggers and routines — each with the identity that changes if a second
// deploy adds a duplicate.
//
// Catalog queries rather than pg_dump: no external binary to find, no version
// skew between client and server, and pg_dump 17 emits a fresh random
// \restrict token on every run that would have to be filtered out anyway.
const fingerprintQuery = `
WITH app AS (
    SELECT oid, nspname FROM pg_namespace
    WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
)
SELECT coalesce(string_agg(line, chr(10) ORDER BY line), '') FROM (
    SELECT format('rel %s.%s %s', a.nspname, c.relname, c.relkind) AS line
    FROM pg_class c JOIN app a ON a.oid = c.relnamespace
    WHERE c.relkind IN ('r', 'v', 'm', 'p', 'i', 'S')
    UNION ALL
    SELECT format('col %s.%s.%s %s', a.nspname, c.relname, att.attname, att.atttypid::regtype)
    FROM pg_attribute att
    JOIN pg_class c ON c.oid = att.attrelid
    JOIN app a ON a.oid = c.relnamespace
    WHERE att.attnum > 0 AND NOT att.attisdropped
    UNION ALL
    SELECT format('con %s.%s %s', a.nspname, c.relname, con.conname)
    FROM pg_constraint con
    JOIN pg_class c ON c.oid = con.conrelid
    JOIN app a ON a.oid = c.relnamespace
    UNION ALL
    SELECT format('pol %s.%s %s', a.nspname, c.relname, p.polname)
    FROM pg_policy p
    JOIN pg_class c ON c.oid = p.polrelid
    JOIN app a ON a.oid = c.relnamespace
    UNION ALL
    SELECT format('trg %s.%s %s', a.nspname, c.relname, tg.tgname)
    FROM pg_trigger tg
    JOIN pg_class c ON c.oid = tg.tgrelid
    JOIN app a ON a.oid = c.relnamespace
    WHERE NOT tg.tgisinternal
    UNION ALL
    SELECT format('fn %s.%s(%s)', a.nspname, pr.proname,
                  pg_get_function_identity_arguments(pr.oid))
    FROM pg_proc pr JOIN app a ON a.oid = pr.pronamespace
) parts`

// schemaFingerprint returns a stable, sorted description of the deployed
// schema, for comparing a database against itself across a redeploy.
func schemaFingerprint(t *testing.T, connString, dbName string) string {
	t.Helper()

	pool := testhelpers.GetTestPool(t, connString, dbName)
	defer pool.Close()

	var fingerprint string
	if err := pool.QueryRow(context.Background(), fingerprintQuery).Scan(&fingerprint); err != nil {
		t.Fatalf("schema fingerprint: %v", err)
	}
	if fingerprint == "" {
		t.Fatal("schema fingerprint is empty, so comparing it would prove nothing")
	}
	return fingerprint
}
