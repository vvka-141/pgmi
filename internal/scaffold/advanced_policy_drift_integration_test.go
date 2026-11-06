package scaffold_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

// Policies live in run-once files, so no redeploy repairs one altered by hand.
// The next deploy fails naming it; restoring it in a new run-once file is
// recorded and passes.
func TestAdvancedTemplate_RefusesHandAlteredPolicy(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, nil)

	const db = "pgmi_advanced_policy_drift"
	defer testhelpers.CleanupTestDB(t, connString, db)
	params := map[string]string{"env": "dev"}
	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	pool := testhelpers.GetTestPool(t, connString, db)
	if _, err := pool.Exec(ctx, "ALTER POLICY invitation_visible ON membership.invitation USING (true)"); err != nil {
		t.Fatalf("alter the policy: %v", err)
	}
	err := deployAdvanced(t, connString, db, projectPath, false, params)
	if err == nil {
		t.Fatal("a deploy over a hand-altered policy succeeded")
	}
	if !strings.Contains(err.Error(), "membership.invitation invitation_visible: changed since ./membership/06-rls.sql ran") {
		t.Fatalf("want the error to name the altered policy, got: %v", err)
	}

	writeProbes(t, projectPath, map[string]string{"membership/90-restore-invitations.sql": `/*
<pgmi-meta id="ffffffff-0451-4000-8000-000000000001" idempotent="false">
  <sortKeys><key>005/000/090</key></sortKeys>
</pgmi-meta>
*/
ALTER POLICY invitation_visible ON membership.invitation
    USING (organization_object_id = (SELECT api.current_organization_id()) OR (SELECT api.is_platform_operator()));
`}, nil)
	if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
		t.Fatalf("a policy restored by a new run-once file must be accepted: %v", err)
	}
}
