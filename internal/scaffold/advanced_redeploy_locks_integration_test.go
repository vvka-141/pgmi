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
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	efs := fakefs.NewEmbedFileSystem(scaffold.GetTemplatesFS(), "templates/advanced")
	testDB := "pgmi_test_redeploy_locks"
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

	reader, err := testhelpers.GetTestPool(t, connString, testDB).Begin(ctx)
	if err != nil {
		t.Fatalf("begin reader: %v", err)
	}
	defer func() { _ = reader.Rollback(ctx) }()
	for _, table := range []string{"membership.organization", "membership.\"user\"", "membership.api_key", "api.handler", "api.rest_route", "api.mcp_route", "api.vw_current_user", "membership.vw_user_claims"} {
		if _, err := reader.Exec(ctx, "SELECT count(*) FROM "+table); err != nil {
			t.Fatalf("reader on %s: %v", table, err)
		}
	}

	cfg.Overwrite, cfg.Force = false, false
	cfg.Timeout = time.Minute
	start := time.Now()
	if err := testhelpers.NewTestDeployerWithFS(t, efs).Deploy(ctx, cfg); err != nil {
		t.Fatalf("redeploy with an open reader failed after %s; something still takes ACCESS EXCLUSIVE on a read table: %v",
			time.Since(start).Round(time.Millisecond), err)
	}
}
