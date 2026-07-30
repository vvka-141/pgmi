package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/contract"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/metadata"
	"github.com/vvka-141/pgmi/internal/params"
)

// pgmi_plan_view.generic_id and metadata.GenerateFallbackID are two
// implementations of one identity, and users key idempotency tracking off the
// first while `pgmi metadata plan` reports the second. They have already
// diverged once (PGMI-329) and nothing checked them against each other.
//
// The invariant only holds because of a chain that is invisible from either
// side, so the obvious cleanups all break it:
//
//   - md5(s.path::bytea) is a CoerceViaIO cast, not a real one: the text is fed
//     to byteain, which parses backslash escapes and the \x hex form. That is
//     harmless only because schema.sql's chk_path_no_backslash rejects every
//     path that could contain one — a constraint in a different file.
//   - md5(convert_to(s.path,'UTF8')) looks like the encoding-correct spelling
//     and is the trap. pgx never sends client_encoding, so a session on a
//     LATIN1 database runs with client_encoding=LATIN1 and PostgreSQL stores
//     pgmi's UTF-8 path bytes verbatim, with no transcode. convert_to then
//     reads those bytes as LATIN1 characters and re-encodes them, so the id
//     changes: ./migrations/café.sql moves from 9bbc3810-… to 08f7ee5f-… on
//     LATIN1 while staying 9bbc3810-… on UTF8. Go keeps hashing UTF-8 bytes,
//     so the two sides silently disagree on exactly the databases no other
//     test covers.
//
// Hence both encodings here. A UTF8-only version of this test passes with
// convert_to substituted in and would not have caught it.
func TestPlanViewGenericIDMatchesGoFallbackAcrossEncodings(t *testing.T) {
	connString := requireTestDB(t)

	projectDir := t.TempDir()
	migrations := filepath.Join(projectDir, "migrations")
	if err := os.MkdirAll(migrations, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Non-ASCII on purpose: an ASCII-only fixture makes every candidate
	// expression agree, and the divergence this pins is only visible on bytes
	// above 0x7F.
	for _, name := range []string{"001_plain.sql", "café.sql", "миграции.sql"} {
		if err := os.WriteFile(filepath.Join(migrations, name), []byte("SELECT 1;\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	scanned, err := scanner.NewScanner(checksum.New()).ScanDirectory(projectDir)
	if err != nil {
		t.Fatalf("scan project: %v", err)
	}
	if len(scanned.Files) != 3 {
		t.Fatalf("scanned %d files, want 3", len(scanned.Files))
	}

	for _, enc := range []struct{ name, encoding string }{
		{"utf8", "UTF8"},
		{"latin1", "LATIN1"},
	} {
		t.Run(enc.name, func(t *testing.T) {
			dbName := "pgmi_itest_generic_id_" + enc.name
			cleanup := createTestDBWithEncoding(t, connString, dbName, enc.encoding)
			defer cleanup()

			pool := connectToTestDB(t, connString, dbName)
			defer pool.Close()

			ctx := context.Background()
			conn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatalf("acquire connection: %v", err)
			}
			defer conn.Release()

			if err := params.CreateSchema(ctx, conn); err != nil {
				t.Fatalf("create schema: %v", err)
			}
			if err := loader.NewLoader().LoadFilesIntoSession(ctx, conn, scanned.Files); err != nil {
				t.Fatalf("load files: %v", err)
			}
			if _, err := contract.Apply(ctx, conn, ""); err != nil {
				t.Fatalf("apply contract: %v", err)
			}

			var serverEnc, clientEnc string
			if err := conn.QueryRow(ctx,
				`SELECT current_setting('server_encoding'), current_setting('client_encoding')`,
			).Scan(&serverEnc, &clientEnc); err != nil {
				t.Fatalf("read encodings: %v", err)
			}
			if serverEnc != enc.encoding {
				t.Fatalf("server_encoding is %s, want %s — the fixture did not take", serverEnc, enc.encoding)
			}

			rows, err := conn.Query(ctx,
				`SELECT DISTINCT path, generic_id FROM pg_temp.pgmi_plan_view ORDER BY path`)
			if err != nil {
				t.Fatalf("query plan view: %v", err)
			}
			defer rows.Close()

			seen := 0
			for rows.Next() {
				var path, genericID string
				if err := rows.Scan(&path, &genericID); err != nil {
					t.Fatalf("scan row: %v", err)
				}
				seen++
				if want := metadata.GenerateFallbackID(path).String(); genericID != want {
					t.Errorf("%s on %s (client_encoding=%s): pgmi_plan_view.generic_id = %s, "+
						"GenerateFallbackID = %s — `pgmi metadata plan` and the deploy "+
						"disagree about this file's identity",
						path, serverEnc, clientEnc, genericID, want)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate rows: %v", err)
			}
			if seen != 3 {
				t.Fatalf("plan view returned %d files, want 3 — the comparison ran on nothing", seen)
			}
		})
	}
}

// createTestDBWithEncoding mirrors createTestDB but pins the server encoding.
// template0 is required: a non-matching encoding cannot be copied from
// template1, and C collation keeps the choice legal for every encoding.
func createTestDBWithEncoding(t *testing.T, connString, dbName, encoding string) func() {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	ident := pgx.Identifier{dbName}.Sanitize()
	if _, err := pool.Exec(ctx, "DROP DATABASE IF EXISTS "+ident); err != nil {
		t.Fatalf("drop database: %v", err)
	}
	create := fmt.Sprintf(
		"CREATE DATABASE %s ENCODING '%s' TEMPLATE template0 LC_COLLATE 'C' LC_CTYPE 'C'",
		ident, encoding)
	if _, err := pool.Exec(ctx, create); err != nil {
		t.Fatalf("create %s database: %v", encoding, err)
	}

	return func() {
		p, err := pgxpool.New(ctx, connString)
		if err != nil {
			return
		}
		defer p.Close()
		_, _ = p.Exec(ctx, "DROP DATABASE IF EXISTS "+ident)
	}
}
