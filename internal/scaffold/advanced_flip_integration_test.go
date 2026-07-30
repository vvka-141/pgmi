package scaffold_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// flipProbe renders the probe migration. The id is stable across both versions —
// that is the whole point: deploy.sql keys its skip decision on the object id,
// so the two versions must be the same script wearing a different contract.
func flipProbe(idempotent bool) string {
	body := "CREATE TABLE IF NOT EXISTS core.flip_probe (id serial PRIMARY KEY, note text);\n"
	if !idempotent {
		// A one-time backfill: appending, so a second run is visible as a row.
		body += "INSERT INTO core.flip_probe (note) VALUES ('backfill');\n"
	}
	return fmt.Sprintf(`/*
<pgmi-meta
    id="ffffffff-0264-4000-8000-000000000001"
    idempotent="%t">
  <description>Flip probe: idempotent on the first deploy, a one-time backfill after.</description>
  <sortKeys>
    <key>900/000</key>
  </sortKeys>
</pgmi-meta>
*/
%s`, idempotent, body)
}

// TestAdvancedTemplate_IdempotentToNonIdempotentFlip covers what the __test__
// harness structurally cannot: the behaviour needs three deploys, and a test
// file gets one.
//
// A script that ran as idempotent (CREATE TABLE IF NOT EXISTS), was then edited
// into a one-time backfill and flipped to idempotent="false", must run exactly
// once more. Matching the execution log on object_id alone would let its own
// earlier idempotent rows skip it forever and the backfill would silently never
// happen; matching on nothing would re-run it on every deploy.
func TestAdvancedTemplate_IdempotentToNonIdempotentFlip(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	projectPath := filepath.Join(t.TempDir(), "flip")
	if err := scaffold.NewScaffolder(false).CreateProject("flip", "advanced", projectPath); err != nil {
		t.Fatalf("scaffold the advanced template: %v", err)
	}

	probePath := filepath.Join(projectPath, "zz_flip_probe.sql")
	writeProbe := func(idempotent bool) {
		t.Helper()
		if err := os.WriteFile(probePath, []byte(flipProbe(idempotent)), 0644); err != nil {
			t.Fatalf("write the probe migration: %v", err)
		}
	}

	const testDB = "pgmi_advanced_flip"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	deployer := testhelpers.NewTestDeployer(t)
	deploy := func(overwrite bool) {
		t.Helper()
		err := deployer.Deploy(context.Background(), pgmi.DeploymentConfig{
			ConnectionString:    connString,
			MaintenanceDatabase: "postgres",
			DatabaseName:        testDB,
			SourcePath:          projectPath,
			Overwrite:           overwrite,
			Force:               true,
			Parameters: map[string]string{
				"database_admin_password":    "TestPassword123!",
				"database_customer_password": "CustomerPassword123!",
				"env":                        "test",
			},
		})
		if err != nil {
			t.Fatalf("deploy failed: %v", err)
		}
	}

	writeProbe(true)
	deploy(true) // fresh database; the probe runs under the idempotent contract

	writeProbe(false)
	deploy(false) // flipped: must run once more, despite its idempotent history
	deploy(false) // and never again

	pool := testhelpers.GetTestPool(t, connString, testDB)

	var rows int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM core.flip_probe").Scan(&rows); err != nil {
		t.Fatalf("read the probe table: %v", err)
	}
	switch {
	case rows == 0:
		t.Error("the flipped script never ran — its earlier idempotent log rows skipped it, " +
			"so a one-time backfill would silently never happen")
	case rows > 1:
		t.Errorf("the flipped script ran %d times — a one-time backfill must run exactly once", rows)
	}

	// The ledger has to show the contract change, not just the row count: this is
	// what the skip predicate reads.
	var idempotentRuns, nonIdempotentRuns int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FILTER (WHERE idempotent),
		       count(*) FILTER (WHERE NOT idempotent)
		FROM internal.deployment_script_execution_log
		WHERE deployment_script_object_id = 'ffffffff-0264-4000-8000-000000000001'
	`).Scan(&idempotentRuns, &nonIdempotentRuns); err != nil {
		t.Fatalf("read the execution log: %v", err)
	}
	if idempotentRuns < 1 {
		t.Errorf("the execution log has no idempotent run for the probe (%d), so the flip was never staged",
			idempotentRuns)
	}
	if nonIdempotentRuns != 1 {
		t.Errorf("the execution log records %d non-idempotent run(s), want exactly 1", nonIdempotentRuns)
	}
}
