package scaffold_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

const catalogTableProbe = `/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000070" idempotent="false">
  <sortKeys><key>900/000</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.probe_status (code text PRIMARY KEY, label text NOT NULL);
CREATE TABLE core.probe_task (id int PRIMARY KEY, status text REFERENCES core.probe_status (code) ON DELETE SET NULL);
CREATE INDEX ix_probe_task_status ON core.probe_task (status);
SELECT pg_temp.exempt_from_rls('core.probe_status', 'test probe');
SELECT pg_temp.exempt_from_rls('core.probe_task', 'test probe');
`

const catalogSyncProbe = `/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000071" idempotent="true">
  <sortKeys><key>900/001</key></sortKeys>
</pgmi-meta>
*/
SELECT pg_temp.sync_catalog('core.probe_status');
`

const catalogFile = "data/catalogs/core.probe_status.json"

func probeStatuses(t *testing.T, connString, db string) string {
	t.Helper()
	var rows string
	if err := testhelpers.GetTestPool(t, connString, db).QueryRow(context.Background(),
		"SELECT string_agg(code || '=' || label, ',' ORDER BY code) FROM core.probe_status").Scan(&rows); err != nil {
		t.Fatalf("read core.probe_status: %v", err)
	}
	return rows
}

// A catalog file is the table's content: each deploy inserts, updates and
// deletes to match it. Dropping a row something references is refused even
// when the foreign key would let the delete through, and a catalog file no
// file syncs fails the deploy instead of being ignored.
func TestAdvancedTemplate_CatalogSync(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, map[string]string{
		"core/catalog_probe.sql":      catalogTableProbe,
		"core/catalog_probe_sync.sql": catalogSyncProbe,
		catalogFile:                   `[{"code": "open", "label": "Open"}, {"code": "done", "label": "Done"}]`,
	})

	const db = "pgmi_advanced_catalog"
	defer testhelpers.CleanupTestDB(t, connString, db)
	params := map[string]string{"env": "dev"}
	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if got := probeStatuses(t, connString, db); got != "done=Done,open=Open" {
		t.Fatalf("after the first deploy core.probe_status = %s", got)
	}

	writeProbes(t, projectPath, map[string]string{
		catalogFile: `[{"code": "open", "label": "Opened"}, {"code": "blocked", "label": "Blocked"}]`,
	}, nil)
	if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
		t.Fatalf("deploy with an edited catalog: %v", err)
	}
	if got := probeStatuses(t, connString, db); got != "blocked=Blocked,open=Opened" {
		t.Fatalf("after editing the catalog core.probe_status = %s", got)
	}

	pool := testhelpers.GetTestPool(t, connString, db)
	if _, err := pool.Exec(ctx, "INSERT INTO core.probe_task VALUES (1, 'blocked')"); err != nil {
		t.Fatalf("reference a catalog row: %v", err)
	}
	writeProbes(t, projectPath, map[string]string{
		catalogFile: `[{"code": "open", "label": "Opened"}]`,
	}, nil)
	err := deployAdvanced(t, connString, db, projectPath, false, params)
	if err == nil {
		t.Fatal("deploy succeeded after dropping a catalog row that is still referenced")
	}
	for _, want := range []string{"./" + catalogFile, "(blocked), still referenced by core.probe_task"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("want the error to contain %q, got: %v", want, err)
		}
	}
	var status *string
	if err := pool.QueryRow(ctx, "SELECT status FROM core.probe_task WHERE id = 1").Scan(&status); err != nil {
		t.Fatalf("read core.probe_task: %v", err)
	}
	if status == nil || *status != "blocked" {
		t.Errorf("the refused deploy changed the referencing row: status = %v", status)
	}

	writeProbes(t, projectPath, map[string]string{
		catalogFile:                          `[{"code": "open", "label": "Opened"}, {"code": "blocked", "label": "Blocked"}]`,
		"data/catalogs/core.probe_kind.json": `[]`,
	}, nil)
	err = deployAdvanced(t, connString, db, projectPath, false, params)
	if err == nil {
		t.Fatal("deploy succeeded with a catalog file no file syncs")
	}
	if !strings.Contains(err.Error(), "./data/catalogs/core.probe_kind.json") {
		t.Errorf("want the error to name ./data/catalogs/core.probe_kind.json, got: %v", err)
	}
}

// internal.vw_deployment has one row per committed deploy and
// internal.vw_deployed_script one per script, with run counts that tell a
// run-once script from a re-run one.
func TestAdvancedTemplate_DeployHistoryViews(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, map[string]string{
		"core/catalog_probe.sql":      catalogTableProbe,
		"core/catalog_probe_sync.sql": catalogSyncProbe,
		catalogFile:                   `[]`,
	})

	const db = "pgmi_advanced_history"
	defer testhelpers.CleanupTestDB(t, connString, db)
	params := map[string]string{"env": "dev"}
	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	writeProbes(t, projectPath, map[string]string{
		"core/catalog_probe_sync.sql": catalogSyncProbe + "SELECT 1;\n",
	}, nil)
	if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
		t.Fatalf("second deploy: %v", err)
	}

	pool := testhelpers.GetTestPool(t, connString, db)
	var login string
	if err := pool.QueryRow(ctx, "SELECT session_user").Scan(&login); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(ctx, `
		SELECT deployed_by, environment, scripts_run, run_once_scripts_run, new_scripts, changed_scripts
		FROM internal.vw_deployment ORDER BY deployed_at, xact_id`)
	if err != nil {
		t.Fatalf("query internal.vw_deployment: %v", err)
	}
	type deployment struct {
		by, env                      string
		run, runOnce, isNew, changed int64
	}
	var got []deployment
	for rows.Next() {
		var d deployment
		if err := rows.Scan(&d.by, &d.env, &d.run, &d.runOnce, &d.isNew, &d.changed); err != nil {
			t.Fatal(err)
		}
		got = append(got, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 deployments, got %d: %+v", len(got), got)
	}
	for i, d := range got {
		if d.by != login || d.env != "dev" {
			t.Errorf("deployment %d: by %s in %s, want %s in dev", i+1, d.by, d.env, login)
		}
	}
	first, second := got[0], got[1]
	if first.run == 0 || first.runOnce == 0 || first.isNew != first.run || first.changed != 0 {
		t.Errorf("first deployment: %+v; want every script new, some run-once, none changed", first)
	}
	if second.run == 0 || second.runOnce != 0 || second.isNew != 0 || second.changed != 1 {
		t.Errorf("second deployment: %+v; want no run-once or new script, one changed", second)
	}

	for _, tc := range []struct {
		path       string
		idempotent bool
		runs       int64
		versions   int64
	}{
		{"./core/catalog_probe.sql", false, 1, 1},
		{"./core/catalog_probe_sync.sql", true, 2, 2},
		{"./lib/internal/views.sql", true, 2, 1},
	} {
		var idempotent, lastDeploy bool
		var runs, versions int64
		if err := pool.QueryRow(ctx, `
			SELECT s.idempotent, s.run_count, s.content_versions,
			       s.last_xact_id = (SELECT max(xact_id) FROM internal.deployment)
			FROM internal.vw_deployed_script s WHERE s.path = $1`, tc.path).Scan(&idempotent, &runs, &versions, &lastDeploy); err != nil {
			t.Fatalf("read %s from internal.vw_deployed_script: %v", tc.path, err)
		}
		if idempotent != tc.idempotent || runs != tc.runs || versions != tc.versions || lastDeploy != tc.idempotent {
			t.Errorf("%s: idempotent=%v runs=%d versions=%d ran in the last deploy=%v; want %v %d %d %v",
				tc.path, idempotent, runs, versions, lastDeploy, tc.idempotent, tc.runs, tc.versions, tc.idempotent)
		}
	}
}
