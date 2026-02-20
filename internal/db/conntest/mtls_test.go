//go:build conntest

package conntest

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/testinfra"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func parseMTLSConnString(t *testing.T) *pgmi.ConnectionConfig {
	t.Helper()
	config, err := db.ParseConnectionString(mtlsContainer.ConnString)
	if err != nil {
		t.Fatalf("parse mTLS connection string: %v", err)
	}
	return config
}

func TestMTLS_ValidClientCert(t *testing.T) {
	config := parseMTLSConnString(t)
	config.SSLMode = "verify-ca"
	config.SSLCert = certPaths.ClientCert
	config.SSLKey = certPaths.ClientKey
	config.SSLRootCert = certPaths.CACert

	pool := connectWithConfig(t, config)
	pingSucceeds(t, pool)

	version := queryVersion(t, pool)
	assert.Contains(t, version, "PostgreSQL")
}

func TestMTLS_NoClientCert(t *testing.T) {
	config := parseMTLSConnString(t)
	config.SSLMode = "require"

	connector, err := db.NewConnector(config)
	require.NoError(t, err)

	_, err = connector.Connect(context.Background())
	require.Error(t, err)
}

func TestMTLS_InvalidClientCert(t *testing.T) {
	otherBundle, err := testinfra.GenerateCertBundle([]string{"localhost"})
	require.NoError(t, err)

	otherDir := t.TempDir()
	otherPaths, err := otherBundle.WriteToDir(otherDir)
	require.NoError(t, err)

	config := parseMTLSConnString(t)
	config.SSLMode = "verify-ca"
	config.SSLCert = otherPaths.ClientCert
	config.SSLKey = otherPaths.ClientKey
	config.SSLRootCert = certPaths.CACert

	connector, err := db.NewConnector(config)
	require.NoError(t, err)

	_, err = connector.Connect(context.Background())
	require.Error(t, err)
}

func TestMTLS_CertFlags_EndToEnd(t *testing.T) {
	config := parseMTLSConnString(t)

	certFlags := &db.CertFlags{
		SSLCert:     certPaths.ClientCert,
		SSLKey:      certPaths.ClientKey,
		SSLRootCert: certPaths.CACert,
	}

	resolved, _, err := db.ResolveConnectionParams(
		db.BuildConnectionString(config),
		nil,
		nil, // Azure flags
		nil, // AWS flags
		nil, // Google flags
		certFlags,
		&db.EnvVars{},
		nil,
	)
	require.NoError(t, err)

	assert.Equal(t, certPaths.ClientCert, resolved.SSLCert)
	assert.Equal(t, certPaths.ClientKey, resolved.SSLKey)
	assert.Equal(t, certPaths.CACert, resolved.SSLRootCert)

	connStr := db.BuildConnectionString(resolved)
	assert.True(t,
		strings.Contains(connStr, "sslcert=") &&
			strings.Contains(connStr, "sslkey=") &&
			strings.Contains(connStr, "sslrootcert="),
		"connection string should contain SSL cert params: %s", connStr)

	pool := connectWithConfig(t, resolved)
	pingSucceeds(t, pool)
}
