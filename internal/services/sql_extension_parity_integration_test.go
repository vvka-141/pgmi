package services

import (
	"context"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// pkg/pgmi/constants.go says twice that SQLExtensions "must stay consistent
// with pg_temp.pgmi_is_sql_file() in schema.sql", and nothing checked it.
//
// The two are not redundant, they are load-bearing in different places. Go's
// list decides whether the scanner parses a file's <pgmi-meta>; the SQL
// function fills is_sql_file, which is what a deploy loop filters on. Let SQL
// recognise an extension Go does not and the file executes with its metadata
// never parsed — an explicit sortKey silently demoted to path order. Let Go
// recognise one SQL does not and the metadata is stored for a file no guarded
// loop will run.
//
// Asked of a live session rather than scraped from the .sql text, so the
// answer is the one deploys actually get.
func TestSQLExtensionParityWithSchema(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_sql_extension_parity"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	prepareSessionTables(t, ctx, conn)

	// Every extension Go knows, plus shapes that must NOT count: a bare name,
	// prose, data, and the editor leftovers that make the is_sql_file guard
	// matter in the first place.
	candidates := []string{
		"x.sql", "x.ddl", "x.dml", "x.dql", "x.dcl", "x.psql", "x.pgsql", "x.plpgsql",
		"x.SQL", "x.Sql", "x.PSQL", "x.PlPgSql",
		"x.md", "x.json", "x.csv", "x.yaml", "x.txt", "x.xml",
		"x.sql.bak", "x.sql~", "x.sql.orig", "x.sqlx", "x.bak", "x",
		"migrations/001.sql", "migrations/001.sql.bak",
	}

	for _, name := range candidates {
		t.Run(name, func(t *testing.T) {
			var fromSQL bool
			if err := conn.QueryRow(ctx, "SELECT pg_temp.pgmi_is_sql_file($1)", name).Scan(&fromSQL); err != nil {
				t.Fatalf("pgmi_is_sql_file(%q): %v", name, err)
			}

			// The Go side classifies by extension, which is what the scanner
			// passes it.
			ext := ""
			if i := strings.LastIndex(name, "."); i >= 0 {
				ext = name[i:]
			}
			fromGo := pgmi.IsSQLExtension(ext)

			if fromGo != fromSQL {
				t.Errorf("%q: pgmi.IsSQLExtension=%v but pg_temp.pgmi_is_sql_file=%v — "+
					"the scanner and the session disagree about whether this file is SQL",
					name, fromGo, fromSQL)
			}
		})
	}
}
