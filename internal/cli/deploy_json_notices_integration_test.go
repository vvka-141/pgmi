package cli

import (
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

// --json carries the deploy's notices, on success and on failure, so an agent
// on the CLI path reads test progress from the record, not from stderr.
func TestDeployJSON_CarriesTheNotices(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	for _, tc := range []struct {
		name, tail string
		wantStatus string
	}{
		{"success", "", "success"},
		{"failure", "SELECT 1/0;\n", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := "pgmi_json_notices_" + tc.name
			t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, db) })
			resetDeployFlags()
			clearPGEnv(t)
			deployFlags.connection = connString
			deployFlags.database = db
			deployFlags.force = true
			deployFlags.jsonOutput = true
			dir := provenanceProject(t, "DO $$ BEGIN RAISE NOTICE 'json-notice-marker'; RAISE WARNING 'json-warning-marker' USING DETAIL = 'json-detail', HINT = 'json-hint'; END $$;\n"+tc.tail)
			env := decodeEnvelope(t, captureStdout(t, func() { _ = runDeploy(deployCmd, []string{dir}) }))
			if env["status"] != tc.wantStatus {
				t.Fatalf("status %v, want %s", env["status"], tc.wantStatus)
			}
			notices, _ := env["notices"].([]any)
			got := map[string]string{}
			for _, n := range notices {
				m, _ := n.(map[string]any)
				got[m["message"].(string)] = m["severity"].(string)
				if m["message"] == "json-warning-marker" && (m["detail"] != "json-detail" || m["hint"] != "json-hint") {
					t.Errorf("the warning must carry its detail and hint: %v", m)
				}
				if _, has := m["detail"]; has && m["message"] == "json-notice-marker" {
					t.Errorf("a notice without detail must have no detail key: %v", m)
				}
			}
			if got["json-notice-marker"] != "NOTICE" || got["json-warning-marker"] != "WARNING" {
				t.Errorf("--json notices = %v, want both markers with their severities", notices)
			}
		})
	}
}
