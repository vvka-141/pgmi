package services

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/pkg/pgmi"
	"github.com/stretchr/testify/require"
)

// TestSessionVariableSystem tests the complete session variable workflow
// including parameter initialization, access patterns, and validation.
func TestSessionVariableSystem(t *testing.T) {
	connStr := getTestConnectionString(t)
	if connStr == "" {
		t.Skip("Skipping integration test: PGMI_TEST_CONN not set")
	}

	tests := []struct {
		name          string
		files         map[string]string
		deploySQL     string
		cliParams     map[string]string
		expectError   bool
		errorContains string
		verifyFunc    func(t *testing.T, conn *pgxpool.Conn)
	}{
		{
			name: "CLI parameters become session variables",
			files: map[string]string{
				"migrations/001_test.sql": "SELECT 1;",
			},
			deploySQL: `
				SELECT pg_temp.pgmi_declare_param('default_env', p_default_value => 'development');
			`,
			cliParams: map[string]string{
				"env":     "production",
				"db_name": "myapp",
			},
			expectError: false,
			verifyFunc: func(t *testing.T, conn *pgxpool.Conn) {
				var envValue, dbNameValue, defaultEnvValue string
				err := conn.QueryRow(context.Background(),
					`SELECT
						current_setting('pgmi.env', true),
						current_setting('pgmi.db_name', true),
						current_setting('pgmi.default_env', true)`).Scan(&envValue, &dbNameValue, &defaultEnvValue)
				require.NoError(t, err)
				require.Equal(t, "production", envValue)
				require.Equal(t, "myapp", dbNameValue)
				require.Equal(t, "development", defaultEnvValue)
			},
		},
		{
			name: "CLI parameters take precedence over defaults",
			files: map[string]string{
				"migrations/001_test.sql": "SELECT 1;",
			},
			deploySQL: `
				SELECT pg_temp.pgmi_declare_param('env', p_default_value => 'development');
				SELECT pg_temp.pgmi_declare_param('api_port', p_default_value => '8080');
			`,
			cliParams: map[string]string{
				"env": "production",
			},
			expectError: false,
			verifyFunc: func(t *testing.T, conn *pgxpool.Conn) {
				var envValue, apiPortValue string
				err := conn.QueryRow(context.Background(),
					`SELECT
						current_setting('pgmi.env', true),
						current_setting('pgmi.api_port', true)`).Scan(&envValue, &apiPortValue)
				require.NoError(t, err)
				require.Equal(t, "production", envValue)
				require.Equal(t, "8080", apiPortValue)
			},
		},
		{
			name: "pgmi_get_param returns values with defaults",
			files: map[string]string{
				"migrations/001_test.sql": "SELECT 1;",
			},
			deploySQL: `
				SELECT pg_temp.pgmi_declare_param('existing_param', p_default_value => 'value1');
			`,
			cliParams:   map[string]string{},
			expectError: false,
			verifyFunc: func(t *testing.T, conn *pgxpool.Conn) {
				var existingValue, missingWithDefault string
				var missingValue *string
				err := conn.QueryRow(context.Background(),
					`SELECT
						pg_temp.pgmi_get_param('existing_param', 'default1'),
						pg_temp.pgmi_get_param('missing_param', NULL),
						pg_temp.pgmi_get_param('missing_param', 'default2')`).
					Scan(&existingValue, &missingValue, &missingWithDefault)
				require.NoError(t, err)
				require.Equal(t, "value1", existingValue)
				require.Nil(t, missingValue)
				require.Equal(t, "default2", missingWithDefault)
			},
		},
		{
			name: "pgmi_declare_param validates type - integer",
			files: map[string]string{
				"migrations/001_test.sql": "SELECT 1;",
			},
			deploySQL: `
				SELECT pg_temp.pgmi_declare_param('port', p_type => 'int');
			`,
			cliParams: map[string]string{
				"port": "not_a_number",
			},
			expectError:   true,
			errorContains: "must be integer",
		},
		{
			name: "pgmi_declare_param validates type - uuid",
			files: map[string]string{
				"migrations/001_test.sql": "SELECT 1;",
			},
			deploySQL: `
				SELECT pg_temp.pgmi_declare_param('request_id', p_type => 'uuid');
			`,
			cliParams: map[string]string{
				"request_id": "not-a-uuid",
			},
			expectError:   true,
			errorContains: "must be valid UUID",
		},
		{
			name: "pgmi_declare_param required without default fails",
			files: map[string]string{
				"migrations/001_test.sql": "SELECT 1;",
			},
			deploySQL: `
				SELECT pg_temp.pgmi_declare_param('api_key', p_required => true);
			`,
			cliParams:     map[string]string{},
			expectError:   true,
			errorContains: "not provided",
		},
		{
			name: "pgmi_declare_param required with CLI value succeeds",
			files: map[string]string{
				"migrations/001_test.sql": "SELECT 1;",
			},
			deploySQL: `
				SELECT pg_temp.pgmi_declare_param('api_key', p_required => true);
			`,
			cliParams: map[string]string{
				"api_key": "secret123",
			},
			expectError: false,
			verifyFunc: func(t *testing.T, conn *pgxpool.Conn) {
				var value string
				err := conn.QueryRow(context.Background(),
					`SELECT current_setting('pgmi.api_key', true)`).Scan(&value)
				require.NoError(t, err)
				require.Equal(t, "secret123", value)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			testDB := createTestDatabase(t, connStr)
			defer dropTestDatabase(t, connStr, testDB)

			mfs := filesystem.NewMemoryFileSystem("/test/project")
			mfs.AddFile("deploy.sql", tt.deploySQL)

			for path, content := range tt.files {
				mfs.AddFile(path, content)
			}

			fileScanner := scanner.NewScannerWithFS(checksum.New(), mfs)
			scanResult, err := fileScanner.ScanDirectory("/test/project")
			require.NoError(t, err)

			testConnStr := replaceDatabase(connStr, testDB)
			pool, err := pgxpool.New(ctx, testConnStr)
			require.NoError(t, err)
			defer pool.Close()

			conn, err := pool.Acquire(ctx)
			require.NoError(t, err)
			defer conn.Release()

			err = createSchemaAndLoadSessionVariables(ctx, conn, scanResult.Files, tt.cliParams)
			require.NoError(t, err)

			_, err = conn.Exec(ctx, tt.deploySQL)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					require.Contains(t, err.Error(), tt.errorContains)
				}
				return
			}

			require.NoError(t, err)

			if tt.verifyFunc != nil {
				tt.verifyFunc(t, conn)
			}
		})
	}
}

func createSchemaAndLoadSessionVariables(ctx context.Context, conn *pgxpool.Conn, filesList []pgmi.FileMetadata, cliParams map[string]string) error {
	if err := params.CreateSchema(ctx, conn); err != nil {
		return err
	}

	fileLoader := loader.NewLoader()

	if err := fileLoader.LoadFilesIntoSession(ctx, conn, filesList); err != nil {
		return err
	}

	return fileLoader.LoadParametersIntoSession(ctx, conn, cliParams)
}

func getTestConnectionString(t *testing.T) string {
	connStr := getEnvOrDefault("PGMI_TEST_CONN", "")
	if connStr == "" {
		t.Skip("PGMI_TEST_CONN environment variable not set")
	}
	return connStr
}

func createTestDatabase(t *testing.T, connStr string) string {
	ctx := context.Background()
	testDB := "pgmi_test_" + time.Now().Format("20060102_150405_000")

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	defer pool.Close()

	_, err = pool.Exec(ctx, "CREATE DATABASE "+testDB)
	require.NoError(t, err)

	return testDB
}

func dropTestDatabase(t *testing.T, connStr string, dbName string) {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Logf("Failed to connect for cleanup: %v", err)
		return
	}
	defer pool.Close()

	_, _ = pool.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", dbName)

	_, err = pool.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName)
	if err != nil {
		t.Logf("Failed to drop test database %s: %v", dbName, err)
	}
}

func replaceDatabase(connStr, newDB string) string {
	config, err := db.ParseConnectionString(connStr)
	if err != nil {
		return connStr
	}
	config.Database = newDB

	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		config.Host, config.Port, config.Username, config.Password, config.Database, config.SSLMode)
}

func getEnvOrDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value != "" {
		return value
	}
	return defaultValue
}
