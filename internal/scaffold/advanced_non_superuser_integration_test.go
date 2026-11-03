package scaffold_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

// The advanced README and docs/PRODUCTION.md promise the template deploys
// without a superuser. This holds them to it: a login with CREATEROLE and
// CREATEDB only (no SUPERUSER, no BYPASSRLS) creates the database, deploys,
// and deploys again.
func TestAdvancedTemplate_DeploysWithoutSuperuser(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	const (
		deployer = "pgmi_nonsuper_deployer"
		password = "deployer-pw"
		db       = "pgmi_advanced_nonsuper"
	)

	admin := testhelpers.GetTestPool(t, connString, "postgres")
	cleanup := func() {
		testhelpers.CleanupTestDB(t, connString, db)
		for _, role := range []string{db + "_admin", db + "_api", db + "_kernel", db + "_owner", deployer} {
			_, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+role)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := admin.Exec(ctx,
		"CREATE ROLE "+deployer+" LOGIN CREATEROLE CREATEDB NOSUPERUSER NOBYPASSRLS PASSWORD '"+password+"'"); err != nil {
		t.Fatalf("create the deployer role: %v", err)
	}
	// A CREATEROLE role administers the roles it creates but cannot SET ROLE to
	// them, which the template's tests do. This setting grants it SET and
	// INHERIT on every role it creates.
	if _, err := admin.Exec(ctx, "ALTER ROLE "+deployer+" SET createrole_self_grant = 'set, inherit'"); err != nil {
		t.Fatalf("configure createrole_self_grant: %v", err)
	}

	var super, bypass bool
	if err := admin.QueryRow(ctx, "SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = $1", deployer).Scan(&super, &bypass); err != nil {
		t.Fatalf("read the deployer's attributes: %v", err)
	}
	if super || bypass {
		t.Fatalf("deployer has SUPERUSER=%v BYPASSRLS=%v; the test must run without both", super, bypass)
	}

	u, err := url.Parse(connString)
	if err != nil {
		t.Fatalf("parse the test connection string: %v", err)
	}
	u.User = url.UserPassword(deployer, password)
	deployerConn := u.String()

	projectPath := scaffoldAdvanced(t, nil)
	params := map[string]string{"env": "dev"}

	if err := deployAdvanced(t, deployerConn, db, projectPath, false, params); err != nil {
		t.Fatalf("first deploy as a non-superuser: %v", err)
	}
	if err := deployAdvanced(t, deployerConn, db, projectPath, false, params); err != nil {
		t.Fatalf("second deploy as a non-superuser: %v", err)
	}
}
