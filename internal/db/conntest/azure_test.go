//go:build azure

package conntest

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func requireAzureEnv(t *testing.T) (host, user, database string) {
	t.Helper()
	host = os.Getenv("PGMI_AZURE_TEST_HOST")
	user = os.Getenv("PGMI_AZURE_TEST_USER")
	database = os.Getenv("PGMI_AZURE_TEST_DB")
	if host == "" || user == "" || database == "" {
		t.Skip("Azure test env vars not set (PGMI_AZURE_TEST_HOST, PGMI_AZURE_TEST_USER, PGMI_AZURE_TEST_DB)")
	}
	return
}

func TestAzure_ServicePrincipal(t *testing.T) {
	host, user, database := requireAzureEnv(t)

	if os.Getenv("AZURE_TENANT_ID") == "" || os.Getenv("AZURE_CLIENT_ID") == "" || os.Getenv("AZURE_CLIENT_SECRET") == "" {
		t.Skip("Azure Service Principal env vars not set")
	}

	config := &pgmi.ConnectionConfig{
		Host:              host,
		Port:              5432,
		Username:          user,
		Database:          database,
		SSLMode:           "require",
		AuthMethod:        pgmi.AuthMethodAzureEntraID,
		AzureTenantID:     os.Getenv("AZURE_TENANT_ID"),
		AzureClientID:     os.Getenv("AZURE_CLIENT_ID"),
		AzureClientSecret: os.Getenv("AZURE_CLIENT_SECRET"),
	}

	connector, err := db.NewConnector(config)
	require.NoError(t, err)

	pool, err := connector.Connect(context.Background())
	require.NoError(t, err)
	defer pool.Close()

	var version string
	err = pool.QueryRow(context.Background(), "SELECT version()").Scan(&version)
	require.NoError(t, err)
	assert.Contains(t, version, "PostgreSQL")
}

func TestAzure_ServicePrincipal_Deploy(t *testing.T) {
	host, user, _ := requireAzureEnv(t)

	if os.Getenv("AZURE_TENANT_ID") == "" || os.Getenv("AZURE_CLIENT_ID") == "" || os.Getenv("AZURE_CLIENT_SECRET") == "" {
		t.Skip("Azure Service Principal env vars not set")
	}

	config := &pgmi.ConnectionConfig{
		Host:              host,
		Port:              5432,
		Username:          user,
		Database:          "postgres",
		SSLMode:           "require",
		AuthMethod:        pgmi.AuthMethodAzureEntraID,
		AzureTenantID:     os.Getenv("AZURE_TENANT_ID"),
		AzureClientID:     os.Getenv("AZURE_CLIENT_ID"),
		AzureClientSecret: os.Getenv("AZURE_CLIENT_SECRET"),
	}

	connStr := db.BuildConnectionString(config)

	deployer := newTestDeployer(t)
	deployConfig := pgmi.DeploymentConfig{
		SourcePath:          t.TempDir(),
		DatabaseName:        "pgmi_azure_deploy_test",
		MaintenanceDatabase: "postgres",
		ConnectionString:    connStr,
		Overwrite:           true,
		Force:               true,
		AuthMethod:          pgmi.AuthMethodAzureEntraID,
		AzureTenantID:       os.Getenv("AZURE_TENANT_ID"),
		AzureClientID:       os.Getenv("AZURE_CLIENT_ID"),
		AzureClientSecret:   os.Getenv("AZURE_CLIENT_SECRET"),
	}

	setupDeployProject(t, deployConfig.SourcePath)

	err := deployer.Deploy(context.Background(), deployConfig)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupDB(t, connStr, deployConfig.DatabaseName)
	})
}

func TestAzure_ManagedIdentity(t *testing.T) {
	if os.Getenv("PGMI_AZURE_MANAGED_IDENTITY") != "true" {
		t.Skip("PGMI_AZURE_MANAGED_IDENTITY not set to true")
	}

	host, user, database := requireAzureEnv(t)

	config := &pgmi.ConnectionConfig{
		Host:       host,
		Port:       5432,
		Username:   user,
		Database:   database,
		SSLMode:    "require",
		AuthMethod: pgmi.AuthMethodAzureEntraID,
	}

	connector, err := db.NewConnector(config)
	require.NoError(t, err)

	pool, err := connector.Connect(context.Background())
	require.NoError(t, err)
	defer pool.Close()

	var version string
	err = pool.QueryRow(context.Background(), "SELECT version()").Scan(&version)
	require.NoError(t, err)
	assert.Contains(t, version, "PostgreSQL")
}
