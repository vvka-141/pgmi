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

func TestStandardConnection_UserPassword(t *testing.T) {
	config := parseStdConnString(t)
	pool := connectWithConfig(t, config)
	pingSucceeds(t, pool)

	version := queryVersion(t, pool)
	assert.Contains(t, version, "PostgreSQL")
}

func TestStandardConnection_WrongPassword(t *testing.T) {
	config := parseStdConnString(t)
	config.Password = "definitely-wrong-password"

	connector, err := db.NewConnector(config)
	require.NoError(t, err)

	_, err = connector.Connect(context.Background())
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "password") ||
			strings.Contains(err.Error(), "authentication"),
		"error should mention authentication: %v", err)
}

func TestStandardConnection_Deploy(t *testing.T) {
	config := parseStdConnString(t)
	config.SSLMode = "disable"

	connStr := db.BuildConnectionString(config)

	deployer := newTestDeployer(t)
	deployConfig := pgmi.DeploymentConfig{
		SourcePath:          t.TempDir(),
		DatabaseName:        "pgmi_conntest_deploy",
		MaintenanceDatabase: testinfra.PostgresDB,
		ConnectionString:    connStr,
		Overwrite:           true,
		Force:               true,
	}

	setupDeployProject(t, deployConfig.SourcePath)

	err := deployer.Deploy(context.Background(), deployConfig)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupDB(t, connStr, deployConfig.DatabaseName)
	})
}
