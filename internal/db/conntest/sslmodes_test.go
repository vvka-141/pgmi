//go:build conntest

package conntest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSSLMode_Disable(t *testing.T) {
	config := parseStdConnString(t)
	config.SSLMode = "disable"

	pool := connectWithConfig(t, config)
	pingSucceeds(t, pool)
}

func TestSSLMode_Require(t *testing.T) {
	config := parseStdConnString(t)
	config.SSLMode = "require"

	pool := connectWithConfig(t, config)
	pingSucceeds(t, pool)

	var ssl bool
	err := pool.QueryRow(t.Context(), "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()").Scan(&ssl)
	if err != nil {
		t.Skipf("pg_stat_ssl not available: %v", err)
	}
	assert.True(t, ssl, "connection should use SSL")
}

func TestSSLMode_VerifyCA(t *testing.T) {
	config := parseStdConnString(t)
	config.SSLMode = "verify-ca"
	config.SSLRootCert = certPaths.CACert

	pool := connectWithConfig(t, config)
	pingSucceeds(t, pool)
}

func TestSSLMode_VerifyFull(t *testing.T) {
	config := parseStdConnString(t)
	config.SSLMode = "verify-full"
	config.SSLRootCert = certPaths.CACert

	pool := connectWithConfig(t, config)
	pingSucceeds(t, pool)
}
