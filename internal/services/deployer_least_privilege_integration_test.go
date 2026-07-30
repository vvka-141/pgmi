package services_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// A CI role is routinely granted CONNECT on its own database and nothing else.
// pgmi used to dial a maintenance database on every deploy -- even when the
// target already existed and nothing needed creating -- so that role got
// "failed to ensure database exists" and exit 11 on a database it could reach
// perfectly well.
func TestDeploy_ExistingDatabase_WithoutMaintenanceDatabaseAccess(t *testing.T) {
	adminConn := testhelpers.RequireDatabase(t)

	const (
		roleName = "pgmi215_ci"
		password = "pgmi215_ci_pw"
		dbName   = "pgmi215_app"
	)

	ctx := context.Background()

	adminPool, err := pgxpool.New(ctx, adminConn)
	if err != nil {
		t.Fatalf("connect as admin: %v", err)
	}
	defer adminPool.Close()

	exec := func(sql string) {
		t.Helper()
		if _, err := adminPool.Exec(ctx, sql); err != nil {
			t.Fatalf("admin exec %q: %v", sql, err)
		}
	}

	// PostgreSQL grants CONNECT on every database to PUBLIC, so denying the
	// maintenance database means revoking it there rather than from the role.
	exec(`DROP DATABASE IF EXISTS ` + dbName)
	exec(`DROP ROLE IF EXISTS ` + roleName)
	exec(`CREATE ROLE ` + roleName + ` LOGIN PASSWORD '` + password + `'`)
	exec(`CREATE DATABASE ` + dbName + ` OWNER ` + roleName)
	exec(`REVOKE CONNECT ON DATABASE postgres FROM PUBLIC`)

	t.Cleanup(func() {
		cleanupPool, err := pgxpool.New(context.Background(), adminConn)
		if err != nil {
			t.Logf("cleanup: connect failed: %v", err)
			return
		}
		defer cleanupPool.Close()
		// WITH (FORCE) is PostgreSQL 13+; terminate first instead so the suite
		// runs on the documented version floor.
		for _, sql := range []string{
			`GRANT CONNECT ON DATABASE postgres TO PUBLIC`,
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			 WHERE datname = ` + "'" + dbName + "'" + ` AND pid <> pg_backend_pid()`,
			`DROP DATABASE IF EXISTS ` + dbName,
			`DROP ROLE IF EXISTS ` + roleName,
		} {
			if _, err := cleanupPool.Exec(context.Background(), sql); err != nil {
				t.Logf("cleanup %q: %v", sql, err)
			}
		}
	})

	// Confirm the denial is real; otherwise this test proves nothing.
	restrictedConfig, err := db.ParseConnectionString(adminConn)
	if err != nil {
		t.Fatalf("parse connection string: %v", err)
	}
	restrictedConfig.Username = roleName
	restrictedConfig.Password = password

	maintenanceConfig := restrictedConfig.DeepCopy()
	maintenanceConfig.Database = "postgres"
	maintenancePool, err := pgxpool.New(ctx, db.BuildConnectionString(&maintenanceConfig))
	if err == nil {
		err = maintenancePool.Ping(ctx)
		maintenancePool.Close()
	}
	if err == nil {
		t.Fatal("role can still reach the maintenance database; the restriction did not apply")
	}

	restrictedConfig.Database = dbName
	projectPath := t.TempDir()
	createMinimalProject(t, projectPath)

	deployer := testhelpers.NewTestDeployer(t)
	err = deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    db.BuildConnectionString(restrictedConfig),
		MaintenanceDatabase: "postgres",
		DatabaseName:        dbName,
		SourcePath:          projectPath,
		Verbose:             testing.Verbose(),
	})
	if err != nil {
		t.Fatalf("deploy to an existing database failed for a role holding CONNECT on "+
			"only that database: %v", err)
	}
}

// The maintenance connection must still be used when the target is genuinely
// absent, so create-if-missing keeps working.
func TestDeploy_MissingDatabase_StillCreatedViaMaintenanceConnection(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	const dbName = "pgmi215_created"

	testhelpers.CleanupTestDB(t, connString, dbName)

	projectPath := t.TempDir()
	createMinimalProject(t, projectPath)

	deployer := testhelpers.NewTestDeployer(t)
	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        dbName,
		SourcePath:          projectPath,
		Verbose:             testing.Verbose(),
	})
	if err != nil {
		t.Fatalf("deploy to a missing database failed: %v", err)
	}
	defer testhelpers.CleanupTestDB(t, connString, dbName)

	pool := testhelpers.GetTestPool(t, connString, dbName)
	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("query the created database: %v", err)
	}
}
