package services_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

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

type noRestoreGaps struct{ *manager.Manager }

func (noRestoreGaps) RestoreGaps(context.Context, db.DBConnection, string) ([]string, error) {
	return nil, nil
}

func TestDeploy_OverwriteRefusesBeforeDropWhenARestoreWouldFail(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	const dbName = "pgmi559_restore"
	const deployer = "pgmi559_deployer"

	admin := testhelpers.GetTestPool(t, connString, "postgres")
	defer admin.Close()
	bg := context.Background()
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(bg, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	testhelpers.CleanupTestDB(t, connString, dbName)
	exec("DROP ROLE IF EXISTS " + deployer)
	exec("CREATE ROLE " + deployer + " LOGIN CREATEDB PASSWORD 'pgmi559'")
	exec(fmt.Sprintf("CREATE DATABASE %s OWNER %s", dbName, deployer))
	exec(fmt.Sprintf("ALTER DATABASE %s SET log_min_duration_statement = 100", dbName))
	exec(fmt.Sprintf("ALTER DATABASE %s SET work_mem = '8MB'", dbName))
	exec(fmt.Sprintf("COMMENT ON DATABASE %s IS 'kept'", dbName))
	t.Cleanup(func() {
		testhelpers.CleanupTestDB(t, connString, dbName)
		_, _ = admin.Exec(bg, "DROP ROLE IF EXISTS "+deployer)
	})

	cfg, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Username, cfg.Password = deployer, "pgmi559"
	asDeployer := db.BuildConnectionString(cfg)

	projectPath := t.TempDir()
	createMinimalProject(t, projectPath)
	deploy := func(mgr services.DatabaseManager) error {
		logger := logging.NewNullLogger()
		return services.NewDeploymentService(
			db.ConnectorFactory(db.Output{}),
			&testhelpers.ForceApprover{},
			logger,
			services.NewSessionManager(db.ConnectorFactory(db.Output{}), scanner.NewScanner(checksum.New()), loader.NewLoader(), logger, "test"),
			mgr,
		).Deploy(bg, pgmi.DeploymentConfig{
			ConnectionString:    asDeployer,
			MaintenanceDatabase: "postgres",
			DatabaseName:        dbName,
			SourcePath:          projectPath,
			Overwrite:           true,
			Force:               true,
		})
	}
	mgr := manager.New()
	before, err := mgr.Settings(bg, db.NewPoolAdapter(admin), dbName)
	if err != nil || before == nil {
		t.Fatalf("settings before: %v, %v", before, err)
	}

	err = deploy(mgr)
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError || !strings.Contains(fmt.Sprint(err), "log_min_duration_statement") {
		t.Fatalf("exit %d, error %v; want exit 10 naming log_min_duration_statement", got, err)
	}
	if strings.Contains(fmt.Sprint(err), "work_mem") {
		t.Errorf("work_mem is restorable by the owner and must not be named: %v", err)
	}
	after, err := mgr.Settings(bg, db.NewPoolAdapter(admin), dbName)
	if err != nil || after == nil || fmt.Sprint(after.Options) != fmt.Sprint(before.Options) || after.Comment == nil {
		t.Fatalf("the refusal must leave the database intact: %+v, %v", after, err)
	}

	err = deploy(noRestoreGaps{mgr})
	if !errors.Is(err, pgmi.ErrExecutionFailed) || !strings.Contains(fmt.Sprint(err), "log_min_duration_statement") {
		t.Fatalf("a restore that fails anyway: %v; want ErrExecutionFailed naming the setting", err)
	}
	after, err = mgr.Settings(bg, db.NewPoolAdapter(admin), dbName)
	if err != nil || after == nil {
		t.Fatalf("settings after: %v, %v", after, err)
	}
	if !slices.Contains(after.Options, "work_mem=8MB") || after.Comment == nil || *after.Comment != "kept" {
		t.Errorf("every other setting must still be restored: options %v, comment %v", after.Options, after.Comment)
	}
}
