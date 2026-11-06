package services_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
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

func TestDeploy_OverwriteRefusesWhileAnotherOverwriteHoldsTheTarget(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const dbName = "pgmi759_overwrite_lock"
	const lockKey = "SELECT hashtextextended('pgmi.overwrite.' || $1, 0)"

	bg := context.Background()
	testhelpers.CleanupTestDB(t, connString, dbName)
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, dbName) })
	admin := testhelpers.GetTestPool(t, connString, "postgres")
	defer admin.Close()
	if _, err := admin.Exec(bg, "CREATE DATABASE "+dbName); err != nil {
		t.Fatal(err)
	}
	target := testhelpers.GetTestPool(t, connString, dbName)
	if _, err := target.Exec(bg, "CREATE TABLE public.survivor ()"); err != nil {
		t.Fatal(err)
	}
	target.Close()

	logger := logging.NewNullLogger()
	deployer := services.NewDeploymentService(
		db.ConnectorFactory(db.Output{}),
		&testhelpers.ForceApprover{},
		logger,
		services.NewSessionManager(db.ConnectorFactory(db.Output{}), scanner.NewScanner(checksum.New()), loader.NewLoader(), logger, "test"),
		manager.New(),
	)
	projectPath := t.TempDir()
	createMinimalProject(t, projectPath)
	overwrite := func() error {
		return deployer.Deploy(bg, pgmi.DeploymentConfig{
			ConnectionString: connString, MaintenanceDatabase: "postgres", DatabaseName: dbName,
			SourcePath: projectPath, Overwrite: true, Force: true,
		})
	}

	holder, err := pgx.Connect(bg, connString)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close(bg)
	if _, err := holder.Exec(bg, "SELECT pg_advisory_lock(("+lockKey+"))", dbName); err != nil {
		t.Fatal(err)
	}
	if err := overwrite(); !errors.Is(err, pgmi.ErrConcurrentDeploy) {
		t.Fatalf("overwrite while another holds the target = %v, want ErrConcurrentDeploy", err)
	}
	var survived bool
	check := testhelpers.GetTestPool(t, connString, dbName)
	if err := check.QueryRow(bg, "SELECT to_regclass('public.survivor') IS NOT NULL").Scan(&survived); err != nil || !survived {
		t.Fatalf("the refused overwrite touched the database: survived=%v err=%v", survived, err)
	}
	check.Close()
	if _, err := holder.Exec(bg, "SELECT pg_advisory_unlock(("+lockKey+"))", dbName); err != nil {
		t.Fatal(err)
	}

	if err := overwrite(); err != nil {
		t.Fatalf("overwrite once the lock is free: %v", err)
	}
	var held int
	if err := admin.QueryRow(bg, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND "+
		"((classid::bigint << 32) | objid::bigint) = ("+lockKey+")", dbName).Scan(&held); err != nil || held != 0 {
		t.Fatalf("the overwrite lock outlived the deploy: %d holder(s), err=%v", held, err)
	}
}
