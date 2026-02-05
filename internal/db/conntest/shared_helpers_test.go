//go:build conntest || azure

package conntest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/logging"
	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

type forceApprover struct{}

func (a *forceApprover) RequestApproval(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func newTestDeployer(t *testing.T) pgmi.Deployer {
	t.Helper()
	logger := logging.NewNullLogger()
	fileScanner := scanner.NewScanner(checksum.New())
	fileLoader := loader.NewLoader()
	dbManager := manager.New()

	sessionManager := services.NewSessionManager(
		db.NewConnector,
		fileScanner,
		fileLoader,
		logger,
	)

	return services.NewDeploymentService(
		db.NewConnector,
		&forceApprover{},
		logger,
		sessionManager,
		fileScanner,
		dbManager,
	)
}

func setupDeployProject(t *testing.T, dir string) {
	t.Helper()
	deploySql := `DO $$ BEGIN RAISE NOTICE 'pgmi connection test deploy'; END $$;`
	err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte(deploySql), 0644)
	if err != nil {
		t.Fatalf("write deploy.sql: %v", err)
	}
}

func cleanupDB(t *testing.T, connStr, dbName string) {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Logf("cleanup: failed to connect: %v", err)
		return
	}
	defer pool.Close()

	_, _ = pool.Exec(ctx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", dbName)
	_, err = pool.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{dbName}.Sanitize())
	if err != nil {
		t.Logf("cleanup: failed to drop %s: %v", dbName, err)
	}
}
