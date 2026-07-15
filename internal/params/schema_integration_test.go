package params_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/params"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
)

func acquireSchemaConn(t *testing.T) (*pgxpool.Conn, func()) {
	t.Helper()
	connStr := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("acquire: %v", err)
	}
	return conn, func() { conn.Release(); pool.Close() }
}

// TestRegisterFile_ExtensionMatchesConstraint pins the fix for the extension
// extraction disagreeing with chk_extension_format. The Go scanner does no name
// filtering, so any of these filenames reaches pgmi_register_file — and before
// the fix a punctuation tail or a trailing dot aborted the whole registration.
func TestRegisterFile_ExtensionMatchesConstraint(t *testing.T) {
	conn, cleanup := acquireSchemaConn(t)
	defer cleanup()

	ctx := context.Background()
	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	const checksum = "abcdef0123456789abcdef0123456789"

	cases := []struct {
		name    string
		want    string
		comment string
	}{
		{"notes.txt", ".txt", "normal extension"},
		{"notes.txt~", "", "editor backup: punctuation tail, no extension"},
		{"notes.", "", "trailing dot: matched the old '\\.' but captured nothing (NULL crash)"},
		{".env-local", "", "leading-dot config with a hyphen tail: no extension"},
		{"archive.tar.gz", ".gz", "only the final segment is the extension"},
		{"Makefile", "", "no dot at all"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ext string
			// register_file must not raise, and must store an extension the
			// table's own CHECK accepts.
			err := conn.QueryRow(ctx,
				`SELECT extension FROM pg_temp.pgmi_register_file($1, 'x', $2, $2)`,
				"./"+tc.name, checksum,
			).Scan(&ext)
			if err != nil {
				t.Fatalf("register_file(%q) failed (%s): %v", tc.name, tc.comment, err)
			}
			if ext != tc.want {
				t.Errorf("register_file(%q) extension = %q, want %q (%s)", tc.name, ext, tc.want, tc.comment)
			}
		})
	}
}

// TestSchema_IsIdempotentWithinASession pins the cleanup-block fix: running
// schema.sql twice in one session must not fail. It used to, because the cleanup
// block dropped neither _pgmi_source_metadata (a FK-referencing table that
// DROP _pgmi_source CASCADE leaves behind) nor the pgmi_test_event type.
func TestSchema_IsIdempotentWithinASession(t *testing.T) {
	conn, cleanup := acquireSchemaConn(t)
	defer cleanup()

	ctx := context.Background()

	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("first CreateSchema: %v", err)
	}
	// The whole point: the SAME session, a second time.
	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("second CreateSchema in the same session must succeed, got: %v", err)
	}

	// And the objects the block previously left stale are healthy afterwards.
	var ok bool
	if err := conn.QueryRow(ctx,
		`SELECT to_regclass('pg_temp._pgmi_source_metadata') IS NOT NULL`).Scan(&ok); err != nil {
		t.Fatalf("probe _pgmi_source_metadata: %v", err)
	}
	if !ok {
		t.Error("_pgmi_source_metadata should exist after a re-run")
	}
}
