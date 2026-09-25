package cli

import (
	"context"
	"fmt"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// `pgmi metadata plan` promises the order pgmi_plan_view will run, so CI can
// assert a reviewed plan without a database. It emitted one row per file at its
// smallest key, which is not what a multi-key file does at deploy (PGMI-383).
// Golden check: the offline rows equal the view's rows, row for row.
func TestMetadataPlan_MatchesPlanView(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	dir := createTestProject(t, map[string]string{
		"deploy.sql": "BEGIN;\nCREATE TABLE public.plan_snapshot AS\n  SELECT execution_order, path, sort_key FROM pg_temp.pgmi_plan_view;\nCOMMIT;\n",
		"schema/roles.sql": "/*\n<pgmi-meta id=\"11112222-3333-4444-5555-666677778888\" idempotent=\"true\">\n" +
			"<sortKeys><key>001/000</key><key>900/000</key></sortKeys>\n</pgmi-meta>\n*/\nSELECT 1;\n",
		"schema/tables.sql":   "/*\n<pgmi-meta id=\"21112222-3333-4444-5555-666677778888\" idempotent=\"true\">\n<sortKeys><key>002/000</key></sortKeys>\n</pgmi-meta>\n*/\nSELECT 1;\n",
		"migrations/001.sql":  "SELECT 1;\n",
		"migrations/002.sql":  "SELECT 1;\n",
		"README.md":           "# readme\n",
		"__test__/test_x.sql": "SELECT 1;\n",
	})

	const testDB = "pgmi_itest_plan_golden"
	defer testhelpers.CleanupTestDB(t, connString, testDB)
	if err := testhelpers.NewTestDeployer(t).Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString: connString, DatabaseName: testDB, SourcePath: dir, Overwrite: true, Force: true,
	}); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	rows, err := testhelpers.GetTestPool(t, connString, testDB).Query(ctx,
		"SELECT execution_order, path, sort_key FROM public.plan_snapshot ORDER BY execution_order")
	if err != nil {
		t.Fatal(err)
	}
	var view []string
	for rows.Next() {
		var n int64
		var path, key string
		if err := rows.Scan(&n, &path, &key); err != nil {
			t.Fatal(err)
		}
		view = append(view, fmt.Sprintf("%d %s %s", n, key, path))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	result, err := planProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	var offline []string
	for _, e := range result.Plan {
		offline = append(offline, fmt.Sprintf("%d %s %s", e.ExecutionOrder, e.SortKey, e.Path))
	}

	if fmt.Sprint(offline) != fmt.Sprint(view) {
		t.Errorf("offline plan differs from pgmi_plan_view\noffline: %q\nview:    %q", offline, view)
	}
}
