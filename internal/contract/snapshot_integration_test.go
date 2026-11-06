package contract_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

const snapshotPath = "testdata/session-api.snapshot"

// Every column of every published view, as PostgreSQL built it. A diff here is
// a change to the session API: update the snapshot on purpose
// (PGMI_UPDATE_SNAPSHOT=1) and list the change under Upgrading in RELEASES.md.
func TestSessionAPI_ColumnSnapshot(t *testing.T) {
	conn := sessionConn(t)
	ctx := context.Background()

	rows, err := conn.Query(ctx, `
		SELECT c.relname::text, a.attname::text, format_type(a.atttypid, a.atttypmod)
		FROM pg_class c
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE c.relnamespace = pg_my_temp_schema()
		  AND c.relkind = 'v'
		  AND c.relname LIKE 'pgmi\_%\_view'
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname COLLATE "C", a.attnum`)
	if err != nil {
		t.Fatalf("read view columns: %v", err)
	}
	var b strings.Builder
	for rows.Next() {
		var view, column, typ string
		if err := rows.Scan(&view, &column, &typ); err != nil {
			t.Fatalf("scan: %v", err)
		}
		fmt.Fprintf(&b, "%s.%s %s\n", view, column, typ)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	got := b.String()

	if os.Getenv("PGMI_UPDATE_SNAPSHOT") == "1" {
		if err := os.WriteFile(snapshotPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write snapshot: %v", err)
		}
		return
	}

	want, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if got != string(want) {
		t.Errorf("session API columns differ from %s.\n"+
			"If the change is intended, rerun with PGMI_UPDATE_SNAPSHOT=1 and list it under Upgrading in RELEASES.md.\n"+
			"--- want\n%s--- got\n%s", snapshotPath, want, got)
	}
}
