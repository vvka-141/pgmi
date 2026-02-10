//go:build conntest

package conntest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vvka-141/pgmi/internal/db"
)

func TestPrecedence_FlagOverridesEnv(t *testing.T) {
	config := parseStdConnString(t)

	t.Setenv("PGPASSWORD", "wrong-password-from-env")

	flagConfig := &db.GranularConnFlags{
		Host:     config.Host,
		Port:     config.Port,
		Username: config.Username,
	}

	envVars := db.LoadFromEnvironment()

	resolved, _, err := db.ResolveConnectionParams(
		"",
		flagConfig,
		nil, // Azure flags
		nil, // AWS flags
		nil, // Cert flags
		envVars,
		nil,
	)
	require.NoError(t, err)

	assert.Equal(t, "wrong-password-from-env", resolved.Password)

	resolved.Password = config.Password
	resolved.Database = config.Database
	resolved.SSLMode = "disable"

	pool := connectWithConfig(t, resolved)
	pingSucceeds(t, pool)
}

func TestPrecedence_CertFlagOverridesEnv(t *testing.T) {
	config := parseMTLSConnString(t)

	t.Setenv("PGSSLCERT", "/nonexistent/wrong.crt")
	t.Setenv("PGSSLKEY", "/nonexistent/wrong.key")

	certFlags := &db.CertFlags{
		SSLCert:     certPaths.ClientCert,
		SSLKey:      certPaths.ClientKey,
		SSLRootCert: certPaths.CACert,
	}

	envVars := db.LoadFromEnvironment()

	resolved, _, err := db.ResolveConnectionParams(
		db.BuildConnectionString(config),
		nil,
		nil, // Azure flags
		nil, // AWS flags
		certFlags,
		envVars,
		nil,
	)
	require.NoError(t, err)

	assert.Equal(t, certPaths.ClientCert, resolved.SSLCert, "flag should override PGSSLCERT env")
	assert.Equal(t, certPaths.ClientKey, resolved.SSLKey, "flag should override PGSSLKEY env")

	pool := connectWithConfig(t, resolved)
	pingSucceeds(t, pool)
}

func TestPrecedence_EnvFallback(t *testing.T) {
	config := parseStdConnString(t)

	t.Setenv("PGHOST", config.Host)
	t.Setenv("PGUSER", config.Username)
	t.Setenv("PGPASSWORD", config.Password)
	t.Setenv("PGSSLMODE", "disable")

	envVars := db.LoadFromEnvironment()

	resolved, _, err := db.ResolveConnectionParams(
		"",
		&db.GranularConnFlags{Port: config.Port},
		nil, // Azure flags
		nil, // AWS flags
		nil, // Cert flags
		envVars,
		nil,
	)
	require.NoError(t, err)

	assert.Equal(t, config.Host, resolved.Host)
	assert.Equal(t, config.Username, resolved.Username)

	resolved.Database = config.Database

	connector, err := db.NewConnector(resolved)
	require.NoError(t, err)

	pool, err := connector.Connect(context.Background())
	require.NoError(t, err)
	defer pool.Close()

	pingSucceeds(t, pool)
}
