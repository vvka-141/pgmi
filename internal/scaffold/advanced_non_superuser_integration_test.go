package scaffold_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
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
		for _, role := range []string{db + "_admin", db + "_api", db + "_kernel", db + "_owner", db + "_jobs", deployer} {
			_, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+role)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := admin.Exec(ctx,
		"CREATE ROLE "+deployer+" LOGIN CREATEROLE CREATEDB NOSUPERUSER NOBYPASSRLS PASSWORD '"+password+"'"); err != nil {
		t.Fatalf("create the deployer role: %v", err)
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
	asDeployer := testhelpers.GetTestPool(t, deployerConn, db)
	defer asDeployer.Close()
	if _, err := asDeployer.Exec(ctx, "CREATE ROLE "+db+"_jobs LOGIN"); err != nil {
		t.Fatalf("the deploy login creates the jobs role: %v", err)
	}
	if _, err := asDeployer.Exec(ctx, "SELECT membership.fn_register_service_principal('service.jobs', 'Background jobs', $1)", db+"_jobs"); err != nil {
		t.Fatalf("register the jobs role: %v", err)
	}
	if err := deployAdvanced(t, deployerConn, db, projectPath, false, params); err != nil {
		t.Fatalf("second deploy as a non-superuser, with a jobs role it created: %v", err)
	}
	var canConnect bool
	if err := admin.QueryRow(ctx, "SELECT has_database_privilege($1, $2, 'CONNECT')", db+"_jobs", db).Scan(&canConnect); err != nil || !canConnect {
		t.Fatalf("the jobs role the deploy login created got no CONNECT: %v, %v", canConnect, err)
	}
}

// A runtime error inside EXECUTE carries the expanded statement as CONTEXT, and
// for the admin role that statement holds the password. A deployer that may not
// alter an admin role another admin created makes ALTER ROLE fail with 42501;
// the raw PostgreSQL error, not just pgmi's redacted rendering, must not carry
// the password, because the server log records the same CONTEXT.
func TestAdvancedTemplate_AdminPasswordNeverInRoleError(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	const (
		deployer = "pgmi_pwleak_deployer"
		password = "deployer-pw"
		db       = "pgmi_advanced_pwleak"
		secret   = "TopSecret_Sentinel_4471"
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

	for _, stmt := range []string{
		"CREATE ROLE " + deployer + " LOGIN CREATEROLE CREATEDB NOSUPERUSER NOBYPASSRLS PASSWORD '" + password + "'",
		"CREATE ROLE " + db + "_owner NOLOGIN",
		"CREATE ROLE " + db + "_admin LOGIN IN ROLE " + db + "_owner",
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	u, err := url.Parse(connString)
	if err != nil {
		t.Fatalf("parse the test connection string: %v", err)
	}
	u.User = url.UserPassword(deployer, password)

	err = deployAdvanced(t, u.String(), db, scaffoldAdvanced(t, nil), false,
		map[string]string{"env": "dev", "database_admin_password": secret})
	if err == nil {
		t.Fatal("deploy succeeded; the deployer must be refused the pre-existing admin role")
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("the deploy error carries no PostgreSQL error: %v", err)
	}
	if pgErr.Code != "42501" {
		t.Fatalf("SQLSTATE %s, want 42501 (the refused ALTER ROLE): %s", pgErr.Code, pgErr.Message)
	}
	for _, channel := range []struct{ name, text string }{
		{"message", pgErr.Message},
		{"detail", pgErr.Detail},
		{"hint", pgErr.Hint},
		{"context", pgErr.Where},
		{"FormatError", pgmi.FormatError(err)},
	} {
		if strings.Contains(channel.text, secret) {
			t.Errorf("the admin password appears in the error's %s:\n%s", channel.name, channel.text)
		}
	}
	if !strings.Contains(pgErr.Message, db+"_admin") {
		t.Errorf("the error does not name the role, so the user cannot tell what failed: %s", pgErr.Message)
	}
}
