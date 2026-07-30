package scaffold_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
)

// appendOnlyOnRedeploy names the tables a second deploy is expected to grow,
// with why. Measured against PG 17.10: deploying the advanced template twice
// changes the exact row count of these three and nothing else.
//
// Everything absent from this map must hold steady. That is what catches a
// seed INSERT missing its ON CONFLICT — a bug this repository has actually had
// — because such a statement duplicates reference data on every deploy without
// failing anything.
var appendOnlyOnRedeploy = map[string]string{
	"internal.deployment_script_execution_log": "audit log: one row per script per deploy, by design",
	"api.rest_exchange":                        "request/response log written by autoLog handlers",
	"api.inbound_queue":                        "queue: the deploy enqueues a demonstration message",
}

// rowCountQuery counts every base table in the project's own schemas exactly.
//
// query_to_xml rather than pg_stat_user_tables: n_live_tup is an estimate
// refreshed by ANALYZE, and it reported 0 for a table holding 2 rows while this
// guard was being written. An assertion resting on stale statistics is worse
// than none.
const rowCountQuery = `
SELECT n.nspname || '.' || c.relname AS name,
       (xpath('/row/c/text()',
              query_to_xml(format('SELECT count(*) AS c FROM %I.%I', n.nspname, c.relname),
                           false, true, '')))[1]::text::bigint AS cnt
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r'
  AND n.nspname NOT LIKE 'pg\_%'
  AND n.nspname <> 'information_schema'`

func rowCounts(t *testing.T, connString, dbName string) map[string]int64 {
	t.Helper()

	pool := testhelpers.GetTestPool(t, connString, dbName)
	defer pool.Close()

	rows, err := pool.Query(context.Background(), rowCountQuery)
	if err != nil {
		t.Fatalf("row counts: %v", err)
	}
	defer rows.Close()

	counts := make(map[string]int64)
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatalf("scan row count: %v", err)
		}
		counts[name] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate row counts: %v", err)
	}
	if len(counts) == 0 {
		t.Fatal("no tables found, so comparing row counts would prove nothing")
	}
	return counts
}

// assertNoDataDrift reports any table that changed across a redeploy, except
// the append-only ones, which may only grow.
func assertNoDataDrift(t *testing.T, before, after map[string]int64) {
	t.Helper()

	var drift []string
	for name, was := range before {
		now, still := after[name]
		if !still {
			drift = append(drift, name+" disappeared")
			continue
		}
		if reason, appendOnly := appendOnlyOnRedeploy[name]; appendOnly {
			if now < was {
				drift = append(drift, name+" shrank, but it is append-only ("+reason+")")
			}
			continue
		}
		if now != was {
			drift = append(drift, name+" changed across a redeploy")
		}
	}
	for name := range after {
		if _, existed := before[name]; !existed {
			drift = append(drift, name+" appeared only after the redeploy")
		}
	}

	if len(drift) > 0 {
		sort.Strings(drift)
		t.Errorf("redeploying changed data that should have been idempotent:\n  %s\n"+
			"A seed INSERT without ON CONFLICT looks exactly like this.",
			strings.Join(drift, "\n  "))
	}
}
