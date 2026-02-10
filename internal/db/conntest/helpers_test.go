//go:build conntest

package conntest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/testinfra"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var (
	stdContainer  *testinfra.PostgresContainer
	mtlsContainer *testinfra.PostgresContainer
	certBundle    *testinfra.CertBundle
	certPaths     *testinfra.CertPaths
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	bundle, err := testinfra.GenerateCertBundle([]string{"localhost", "127.0.0.1"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate certs: %v\n", err)
		os.Exit(1)
	}
	certBundle = bundle

	dir, err := os.MkdirTemp("", "pgmi-conntest-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}

	paths, err := bundle.WriteToDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "write certs: %v\n", err)
		os.Exit(1)
	}
	certPaths = paths

	std, err := testinfra.StartPostgres(ctx, certPaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres: %v\n", err)
		os.Exit(1)
	}
	stdContainer = std

	mtls, err := testinfra.StartMTLSPostgres(ctx, certPaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start mTLS postgres: %v\n", err)
		stdContainer.Terminate(ctx) //nolint:errcheck
		os.Exit(1)
	}
	mtlsContainer = mtls

	code := m.Run()

	stdContainer.Terminate(ctx)  //nolint:errcheck
	mtlsContainer.Terminate(ctx) //nolint:errcheck
	os.RemoveAll(dir)
	os.Exit(code)
}

func connectWithConfig(t *testing.T, config *pgmi.ConnectionConfig) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	connector, err := db.NewConnector(config)
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	pool, err := connector.Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	t.Cleanup(func() { pool.Close() })
	return pool
}

func pingSucceeds(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	err := pool.Ping(context.Background())
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}
}

func queryVersion(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var version string
	err := pool.QueryRow(context.Background(), "SELECT version()").Scan(&version)
	if err != nil {
		t.Fatalf("query version: %v", err)
	}
	return version
}

func parseStdConnString(t *testing.T) *pgmi.ConnectionConfig {
	t.Helper()
	config, err := db.ParseConnectionString(stdContainer.ConnString)
	if err != nil {
		t.Fatalf("parse connection string: %v", err)
	}
	return config
}
