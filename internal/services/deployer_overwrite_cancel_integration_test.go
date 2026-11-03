package services_test

import (
	"context"
	"errors"
	"fmt"
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

type cancelAfterDrop struct {
	*manager.Manager
	cancel context.CancelFunc
}

func (m *cancelAfterDrop) Drop(ctx context.Context, conn pgmi.DBConnection, dbName string) error {
	err := m.Manager.Drop(ctx, conn, dbName)
	m.cancel()
	return err
}

func TestDeploy_OverwriteCancelledAfterDropStillRecreatesTheDatabase(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	const dbName = "pgmi409_cancel_window"
	const ownerRole = "pgmi409_owner"
	const readerRole = "pgmi409_reader"

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
	exec("DROP ROLE IF EXISTS " + ownerRole)
	exec("DROP ROLE IF EXISTS " + readerRole)
	exec("CREATE ROLE " + ownerRole)
	exec("CREATE ROLE " + readerRole)
	exec(fmt.Sprintf("CREATE DATABASE %s OWNER %s CONNECTION LIMIT 17", dbName, ownerRole))
	exec(fmt.Sprintf("ALTER DATABASE %s SET statement_timeout = '9s'", dbName))
	exec(fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM PUBLIC", dbName))
	exec(fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", dbName, readerRole))
	t.Cleanup(func() {
		testhelpers.CleanupTestDB(t, connString, dbName)
		_, _ = admin.Exec(bg, "DROP ROLE IF EXISTS "+ownerRole)
		_, _ = admin.Exec(bg, "DROP ROLE IF EXISTS "+readerRole)
	})

	mgr := manager.New()
	before, err := mgr.Settings(bg, db.NewPoolAdapter(admin), dbName)
	if err != nil || before == nil {
		t.Fatalf("settings before overwrite: %v, %v", before, err)
	}

	ctx, cancel := context.WithCancel(bg)
	defer cancel()

	logger := logging.NewNullLogger()
	deployer := services.NewDeploymentService(
		db.NewConnector,
		&testhelpers.ForceApprover{},
		logger,
		services.NewSessionManager(db.NewConnector, scanner.NewScanner(checksum.New()), loader.NewLoader(), logger, "test"),
		&cancelAfterDrop{Manager: mgr, cancel: cancel},
	)

	projectPath := t.TempDir()
	createMinimalProject(t, projectPath)
	err = deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        dbName,
		SourcePath:          projectPath,
		Overwrite:           true,
		Force:               true,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("deploy error = %v, want context.Canceled", err)
	}

	after, err := mgr.Settings(bg, db.NewPoolAdapter(admin), dbName)
	if err != nil {
		t.Fatalf("settings after overwrite: %v", err)
	}
	if after == nil {
		t.Fatal("a cancel between DROP and CREATE lost the database")
	}
	if after.Owner != ownerRole {
		t.Errorf("owner = %q, want %q", after.Owner, ownerRole)
	}
	if after.ConnectionLimit != 17 {
		t.Errorf("connection limit = %d, want 17", after.ConnectionLimit)
	}
	if fmt.Sprint(after.Options) != fmt.Sprint(before.Options) {
		t.Errorf("options = %v, want %v", after.Options, before.Options)
	}
	if !after.CustomACL || fmt.Sprint(after.Grants) != fmt.Sprint(before.Grants) {
		t.Errorf("grants = %v (custom %v), want %v", after.Grants, after.CustomACL, before.Grants)
	}
}
