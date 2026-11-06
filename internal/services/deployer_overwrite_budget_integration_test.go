package services_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/logging"
	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func overwriteAs(t *testing.T, connString, dbName string, mgr services.DatabaseManager) error {
	t.Helper()
	projectPath := t.TempDir()
	createMinimalProject(t, projectPath)
	logger := logging.NewNullLogger()
	return services.NewDeploymentService(
		db.ConnectorFactory(db.Output{}),
		&testhelpers.ForceApprover{},
		logger,
		services.NewSessionManager(db.ConnectorFactory(db.Output{}), scanner.NewScanner(checksum.New()), loader.NewLoader(), logger, "test"),
		mgr,
	).Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString: connString, MaintenanceDatabase: "postgres", DatabaseName: dbName,
		SourcePath: projectPath, Overwrite: true, Force: true,
	})
}

func TestDeploy_OverwriteFitsTwoConnections(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const dbName = "pgmi763_budget"
	const role = "pgmi763_lim2"

	bg := context.Background()
	admin := testhelpers.GetTestPool(t, connString, "postgres")
	defer admin.Close()
	testhelpers.CleanupTestDB(t, connString, dbName)
	_, _ = admin.Exec(bg, "DROP ROLE IF EXISTS "+role)
	if _, err := admin.Exec(bg, "CREATE ROLE "+role+" LOGIN CREATEDB CONNECTION LIMIT 2 PASSWORD 'pgmi763'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testhelpers.CleanupTestDB(t, connString, dbName)
		_, _ = admin.Exec(bg, "DROP ROLE IF EXISTS "+role)
	})

	cfg, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Username, cfg.Password = role, "pgmi763"
	asRole := db.BuildConnectionString(cfg)

	if err := overwriteAs(t, asRole, dbName, manager.New()); err != nil {
		t.Fatalf("--overwrite of a missing target under CONNECTION LIMIT 2: %v", err)
	}
	if err := overwriteAs(t, asRole, dbName, manager.New()); err != nil {
		t.Fatalf("--overwrite of an existing target under CONNECTION LIMIT 2: %v", err)
	}
}

type lockProbe struct {
	*manager.Manager
	admin    *pgxpool.Pool
	heldAtCr []bool
}

func (p *lockProbe) Create(ctx context.Context, conn db.DBConnection, dbName string, settings *manager.DatabaseSettings) error {
	var held bool
	err := p.admin.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_locks WHERE locktype = 'advisory' AND granted AND "+
		"((classid::bigint << 32) | objid::bigint) = hashtextextended('pgmi.overwrite.' || $1, 0))", dbName).Scan(&held)
	if err != nil {
		return err
	}
	p.heldAtCr = append(p.heldAtCr, held)
	return p.Manager.Create(ctx, conn, dbName, settings)
}

func TestDeploy_OverwriteOfAMissingTargetCreatesUnderTheLock(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const dbName = "pgmi763_missing"

	admin := testhelpers.GetTestPool(t, connString, "postgres")
	defer admin.Close()
	testhelpers.CleanupTestDB(t, connString, dbName)
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, dbName) })

	probe := &lockProbe{Manager: manager.New(), admin: admin}
	if err := overwriteAs(t, connString, dbName, probe); err != nil {
		t.Fatal(err)
	}
	if len(probe.heldAtCr) != 1 || !probe.heldAtCr[0] {
		t.Fatalf("overwrite lock held at CREATE DATABASE = %v, want [true]", probe.heldAtCr)
	}
}
