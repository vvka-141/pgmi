package scaffold_test

import (
	"context"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/files/fakefs"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// An unchanged redeploy of the advanced template must not take ACCESS
// EXCLUSIVE on the tables the application reads. It used to rebuild every RLS
// policy and re-run evolution-path ALTER TABLEs, so a reader of
// membership.organization or api.handler stalled the deploy and every query
// queued behind it for the rest of the deploy (PGMI-374). With the reader's
// ACCESS SHARE held open, a deploy that still needed the lock fails after the
// template's 5s lock_timeout (55P03) instead of completing.
func TestAdvancedTemplate_RedeployDoesNotLockReadTables(t *testing.T) {
	redeployWhileHolding(t, "pgmi_test_redeploy_locks", "ACCESS SHARE", []string{
		"membership.organization", "membership.principal", "membership.permission", "api.handler",
		"api.rest_route", "api.mcp_route", "api.vw_current_user", "membership.vw_user_principal",
	})
}

// The application writes the exchange log and the identity tables at runtime,
// so an open write transaction holds ROW EXCLUSIVE on them.
// A redeploy that re-ran CREATE INDEX IF NOT EXISTS took SHARE before looking
// at IF NOT EXISTS, queued behind those writers and failed with 55P03
// (PGMI-393). Indexes now live in run-once files, so a redeploy creates none.
func TestAdvancedTemplate_RedeployDoesNotBlockWriters(t *testing.T) {
	redeployWhileHolding(t, "pgmi_test_redeploy_writers", "ROW EXCLUSIVE", []string{
		"api.exchange", "api.rest_exchange", "api.rpc_exchange", "api.mcp_exchange",
		"membership.principal", "membership.user_principal", "membership.external_identity",
		"membership.organization", "membership.organization_membership", "membership.role_assignment",
		"membership.audit_event",
	})
}

// redeployWhileHolding deploys the advanced template, opens a second session
// holding mode on each table, and redeploys. A deploy that still needs a
// conflicting lock fails after the template's 5s lock_timeout instead of
// completing.
func redeployWhileHolding(t *testing.T, testDB, mode string, tables []string) {
	t.Helper()
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	efs := fakefs.NewEmbedFileSystem(scaffold.GetTemplatesFS(), "templates/advanced")
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	cfg := pgmi.DeploymentConfig{
		ConnectionString: connString,
		DatabaseName:     testDB,
		SourcePath:       ".",
		Overwrite:        true,
		Force:            true,
		Parameters:       map[string]string{"database_admin_password": "TestPassword123!", "env": "test"},
	}
	if err := testhelpers.NewTestDeployerWithFS(t, efs).Deploy(ctx, cfg); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	holder, err := testhelpers.GetTestPool(t, connString, testDB).Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	for _, table := range tables {
		if _, err := holder.Exec(ctx, "LOCK TABLE "+table+" IN "+mode+" MODE"); err != nil {
			t.Fatalf("lock %s in %s mode: %v", table, mode, err)
		}
	}

	cfg.Overwrite, cfg.Force = false, false
	bounded, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	start := time.Now()
	if err := testhelpers.NewTestDeployerWithFS(t, efs).Deploy(bounded, cfg); err != nil {
		t.Fatalf("redeploy while another session holds %s failed after %s; the deploy still takes a conflicting lock: %v",
			mode, time.Since(start).Round(time.Millisecond), err)
	}
}
