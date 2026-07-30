package services

import (
	"context"
	"slices"
	"testing"

	"github.com/vvka-141/pgmi/internal/ai"
	"github.com/vvka-141/pgmi/internal/contract"
)

// TestContractViewColumnsMatchLiveSession checks ai.GetContract() against the
// columns a real session actually exposes.
//
// The static guard in internal/ai scrapes CREATE TABLE statements with a regex
// and skips any view it cannot map to a backing table — which is every derived
// view. pgmi_plan_view is the only view whose column list is hand-written in
// SQL rather than inherited from a table, so it is both the most likely to
// drift and the one nothing was checking.
func TestContractViewColumnsMatchLiveSession(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_contract_columns"
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
	if _, err := contract.Apply(ctx, conn, ""); err != nil {
		t.Fatalf("apply contract: %v", err)
	}

	live := map[string][]string{}
	rows, err := conn.Query(ctx, `
		SELECT c.relname, array_agg(a.attname ORDER BY a.attnum)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		WHERE n.nspname = pg_my_temp_schema()::regnamespace::text
		  AND c.relkind = 'v'
		GROUP BY c.relname`)
	if err != nil {
		t.Fatalf("query view columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var cols []string
		if err := rows.Scan(&name, &cols); err != nil {
			t.Fatalf("scan: %v", err)
		}
		live[name] = cols
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate view columns: %v", err)
	}

	declared := map[string]bool{}
	for _, v := range ai.GetContract().Views {
		declared[v.Name] = true

		got, ok := live[v.Name]
		if !ok {
			t.Errorf("view %q is in the contract but the session does not create it", v.Name)
			continue
		}
		// Order matters: deploy.sql may SELECT * INTO a record.
		if !slices.Equal(v.Columns, got) {
			t.Errorf("view %q: contract says %v, session has %v", v.Name, v.Columns, got)
		}
	}

	for name := range live {
		if !declared[name] {
			t.Errorf("session publishes view %q, which the contract does not declare", name)
		}
	}
}
