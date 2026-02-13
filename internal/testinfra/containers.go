package testinfra

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	PostgresImage    = "alexeye/postgres-azure-flex:17"
	PostgresUser     = "postgres"
	PostgresPassword = "postgres"
	PostgresDB       = "postgres"

	containerCertDir  = "/tmp/testcontainers-go/postgres"
	sslEntrypointPath = "/usr/local/bin/docker-entrypoint-ssl.bash"
)

type PostgresContainer struct {
	*postgres.PostgresContainer
	ConnString string
}

func StartPostgres(ctx context.Context, certPaths *CertPaths) (*PostgresContainer, error) {
	confPath, err := writeSSLConfig(filepath.Dir(certPaths.CACert))
	if err != nil {
		return nil, err
	}

	ctr, err := postgres.Run(ctx,
		PostgresImage,
		postgres.WithUsername(PostgresUser),
		postgres.WithPassword(PostgresPassword),
		postgres.WithDatabase(PostgresDB),
		postgres.WithSSLCert(certPaths.CACert, certPaths.ServerCert, certPaths.ServerKey),
		postgres.WithConfigFile(confPath),
		// WithSSLCert sets entrypoint to "sh" which fails on Debian (dash doesn't support pipefail).
		testcontainers.WithEntrypoint("bash", sslEntrypointPath),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("start postgres: %w", err)
	}

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		ctr.Terminate(ctx) //nolint:errcheck
		return nil, fmt.Errorf("get connection string: %w", err)
	}

	return &PostgresContainer{PostgresContainer: ctr, ConnString: connStr}, nil
}

func StartMTLSPostgres(ctx context.Context, certPaths *CertPaths) (*PostgresContainer, error) {
	dir := filepath.Dir(certPaths.CACert)

	confPath, err := writeSSLConfig(dir)
	if err != nil {
		return nil, err
	}

	initScript, err := writeMTLSInitScript(dir)
	if err != nil {
		return nil, err
	}

	ctr, err := postgres.Run(ctx,
		PostgresImage,
		postgres.WithUsername(PostgresUser),
		postgres.WithPassword(PostgresPassword),
		postgres.WithDatabase(PostgresDB),
		postgres.WithSSLCert(certPaths.CACert, certPaths.ServerCert, certPaths.ServerKey),
		postgres.WithConfigFile(confPath),
		postgres.WithInitScripts(initScript),
		// WithSSLCert sets entrypoint to "sh" which fails on Debian (dash doesn't support pipefail).
		testcontainers.WithEntrypoint("bash", sslEntrypointPath),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("start mTLS postgres: %w", err)
	}

	connStr, err := ctr.ConnectionString(ctx, "sslmode=verify-ca")
	if err != nil {
		ctr.Terminate(ctx) //nolint:errcheck
		return nil, fmt.Errorf("get connection string: %w", err)
	}

	return &PostgresContainer{PostgresContainer: ctr, ConnString: connStr}, nil
}

func StartSimplePostgres(ctx context.Context) (*PostgresContainer, error) {
	ctr, err := postgres.Run(ctx,
		PostgresImage,
		postgres.WithUsername(PostgresUser),
		postgres.WithPassword(PostgresPassword),
		postgres.WithDatabase(PostgresDB),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("start postgres: %w", err)
	}

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		ctr.Terminate(ctx) //nolint:errcheck
		return nil, fmt.Errorf("get connection string: %w", err)
	}

	return &PostgresContainer{PostgresContainer: ctr, ConnString: connStr}, nil
}

func writeSSLConfig(dir string) (string, error) {
	conf := fmt.Sprintf(`listen_addresses = '*'
ssl = on
ssl_cert_file = '%s/server.cert'
ssl_key_file = '%s/server.key'
ssl_ca_file = '%s/ca_cert.pem'
`, containerCertDir, containerCertDir, containerCertDir)

	path := filepath.Join(dir, "postgresql.conf")
	if err := os.WriteFile(path, []byte(conf), 0644); err != nil {
		return "", fmt.Errorf("write postgresql.conf: %w", err)
	}
	return path, nil
}

func writeMTLSInitScript(dir string) (string, error) {
	script := `#!/bin/bash
cat > "$PGDATA/pg_hba.conf" << 'PGEOF'
local   all all                trust
hostssl all all 0.0.0.0/0      cert clientcert=verify-full
hostssl all all ::/0            cert clientcert=verify-full
PGEOF
`
	path := filepath.Join(dir, "init-mtls.sh")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		return "", fmt.Errorf("write init script: %w", err)
	}
	return path, nil
}
