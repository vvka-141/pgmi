package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/logging"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/internal/ui"
	"github.com/vvka-141/pgmi/pkg/pgmi"
	"github.com/spf13/cobra"
)

var testCmd = &cobra.Command{
	Use:   "test <project_path>",
	Short: "Execute database tests",
	Long: `Test executes database unit tests using the SQL files in the specified directory.

The test command:
1. Connects to the target database (must already exist)
2. Loads SQL files into pg_temp.pgmi_source
3. Loads test files into pg_temp.pgmi_unittest_plan
4. Executes tests filtered by the provided pattern

IMPORTANT: The test command does NOT deploy code or modify database schema.
           Use 'pgmi deploy' to set up the database before running tests.

Test Discovery:
  Tests are automatically discovered from directories containing '/__test__/'.
  Example structure:
    ./migrations/schema.sql              (NOT executed by test command)
    ./migrations/__test__/test_schema.sql (executed by test command)
    ./migrations/__test__/_setup.sql      (runs before tests in this directory)

Password Authentication:
  For security, password is NOT accepted as a CLI flag. Use one of:
    1. $PGPASSWORD environment variable
    2. .pgpass file (PostgreSQL standard: chmod 600 ~/.pgpass)
    3. Connection string: postgresql://user:pass@host/db

Arguments:
  project_path    Path to directory containing SQL files and tests

Examples:
  # Run all tests
  pgmi test ./myapp -d test_db

  # Run only pre-deployment tests
  pgmi test ./myapp -d test_db --filter "/pre-deployment/"

  # Run auth-related tests
  pgmi test ./myapp -d test_db --filter "/__test__/auth/"

  # List tests without executing
  pgmi test ./myapp -d test_db --list

  # Run tests with parameters
  pgmi test ./myapp -d test_db --param test_user_id=123`,
	Args: cobra.ExactArgs(1),
	RunE: runTest,
}

var (
	// Connection parameters
	testConnection string
	testHost       string
	testPort       int
	testUsername   string
	testDatabase   string
	testSSLMode    string

	// Azure Entra ID parameters
	testAzureTenantID string
	testAzureClientID string

	// Test options
	testFilter     string
	testList       bool
	testParams     []string
	testParamsFile string
)

func init() {
	rootCmd.AddCommand(testCmd)

	// Connection string flag (mutually exclusive with granular flags)
	testCmd.Flags().StringVar(&testConnection, "connection", "",
		"PostgreSQL connection string (URI or ADO.NET format).\n"+
			"Mutually exclusive with granular flags (--host, --port, --username).\n"+
			"Alternative: Use PGMI_CONNECTION_STRING or DATABASE_URL environment variable.\n"+
			"Example: postgresql://user:pass@localhost:5432/test_db")

	// Granular connection flags (PostgreSQL standard)
	testCmd.Flags().StringVar(&testHost, "host", "",
		"PostgreSQL server host (default: localhost, or $PGHOST)")
	testCmd.Flags().IntVarP(&testPort, "port", "p", 0,
		"PostgreSQL server port (default: 5432, or $PGPORT)")
	testCmd.Flags().StringVarP(&testUsername, "username", "U", "",
		"PostgreSQL user (default: $PGUSER or current OS user)")
	testCmd.Flags().StringVarP(&testDatabase, "database", "d", "",
		"Target database name (optional if specified in connection string, or $PGDATABASE)")
	testCmd.Flags().StringVar(&testSSLMode, "sslmode", "",
		"SSL mode: disable|allow|prefer|require|verify-ca|verify-full\n"+
			"(default: prefer, or $PGSSLMODE)")

	// Azure Entra ID flags
	testCmd.Flags().StringVar(&testAzureTenantID, "azure-tenant-id", "",
		"Azure AD tenant/directory ID (overrides $AZURE_TENANT_ID)")
	testCmd.Flags().StringVar(&testAzureClientID, "azure-client-id", "",
		"Azure AD application/client ID (overrides $AZURE_CLIENT_ID)")

	// Test options
	testCmd.Flags().StringVar(&testFilter, "filter", ".*", "POSIX regex pattern to filter tests (default: \".*\" matches all)\n"+
		"Examples:\n"+
		"  --filter \"/pre-deployment/\"           # Tests in /pre-deployment/ directories\n"+
		"  --filter \".*_integration\\.sql$\"       # Integration tests\n"+
		"  --filter \"/__test__/auth/\"             # Tests under __test__/auth/")
	testCmd.Flags().BoolVar(&testList, "list", false, "List tests without executing them (dry-run mode)")
	testCmd.Flags().StringSliceVar(&testParams, "param", nil, "Parameters as key=value pairs (for parameterized tests)")
	testCmd.Flags().StringVar(&testParamsFile, "params-file", "", "Load parameters from .env file")
}

// buildTestConfig builds a TestConfig from CLI flags and environment.
// This function is extracted for testability and separation of concerns.
//
// Parameters:
//   - sourcePath: Path to the test directory
//   - verbose: Enable verbose logging
//
// Returns:
//   - Fully configured TestConfig ready for test execution
//   - Error if configuration is invalid
func buildTestConfig(sourcePath string, verbose bool) (pgmi.TestConfig, error) {
	// Load .env file if it exists (silent fail if not present)
	_ = godotenv.Load()

	// Resolve connection parameters
	granularFlags := &db.GranularConnFlags{
		Host:     testHost,
		Port:     testPort,
		Username: testUsername,
		Database: testDatabase,
		SSLMode:  testSSLMode,
	}

	azureFlags := &db.AzureFlags{
		TenantID: testAzureTenantID,
		ClientID: testAzureClientID,
	}

	connConfig, _, err := resolveConnection(testConnection, granularFlags, azureFlags, verbose)
	if err != nil {
		return pgmi.TestConfig{}, err
	}

	// Resolve target database: -d flag always takes precedence over connection string
	targetDB, err := resolveTargetDatabase(
		testDatabase,
		connConfig.Database,
		true, // require database
		"test",
		verbose,
	)
	if err != nil {
		return pgmi.TestConfig{}, err
	}

	// Update config with resolved target database
	// Note: Test command doesn't need maintenance DB handling since it doesn't create databases
	connConfig.Database = targetDB

	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Connection resolved:\n")
		fmt.Fprintf(os.Stderr, "  Host: %s\n", connConfig.Host)
		fmt.Fprintf(os.Stderr, "  Port: %d\n", connConfig.Port)
		fmt.Fprintf(os.Stderr, "  User: %s\n", connConfig.Username)
		fmt.Fprintf(os.Stderr, "  Target Database: %s\n", connConfig.Database)
		fmt.Fprintf(os.Stderr, "  SSL Mode: %s\n", connConfig.SSLMode)
		fmt.Fprintf(os.Stderr, "  Auth Method: %s\n", connConfig.AuthMethod)
		fmt.Fprintf(os.Stderr, "[VERBOSE] Source path: %s\n", sourcePath)
		fmt.Fprintf(os.Stderr, "[VERBOSE] Filter pattern: %s\n", testFilter)
	}

	// Parse parameters from file (if provided)
	parameters := make(map[string]string)
	if testParamsFile != "" {
		fsProvider := filesystem.NewOSFileSystem()
		fileParams, err := loadParamsFromFiles(fsProvider, []string{testParamsFile}, verbose)
		if err != nil {
			return pgmi.TestConfig{}, err
		}
		parameters = fileParams
	}

	// Parse CLI parameters (these override file parameters)
	cliParams, err := params.ParseKeyValuePairs(testParams)
	if err != nil {
		return pgmi.TestConfig{}, fmt.Errorf("invalid parameter format: %w", err)
	}

	// Merge CLI params into file params (CLI takes precedence)
	for k, v := range cliParams {
		parameters[k] = v
	}

	// Build connection string for test execution
	connStr := db.BuildConnectionString(connConfig)

	// Create test configuration
	config := pgmi.TestConfig{
		SourcePath:        sourcePath,
		DatabaseName:      connConfig.Database,
		ConnectionString:  connStr,
		FilterPattern:     testFilter,
		ListOnly:          testList,
		Parameters:        parameters,
		Verbose:           verbose,
		AuthMethod:        connConfig.AuthMethod,
		AzureTenantID:     connConfig.AzureTenantID,
		AzureClientID:     connConfig.AzureClientID,
		AzureClientSecret: connConfig.AzureClientSecret,
	}

	return config, nil
}

func runTest(cmd *cobra.Command, args []string) error {
	sourcePath := args[0]
	verbose := getVerboseFlag(cmd)

	// Build test configuration
	config, err := buildTestConfig(sourcePath, verbose)
	if err != nil {
		return err
	}

	// Create dependencies (reuse deployment service infrastructure)
	logger := logging.NewConsoleLogger(verbose)
	fileScanner := scanner.NewScanner(checksum.New())
	fileLoader := loader.NewLoader()
	dbManager := manager.New()

	// Test command doesn't need approver (no overwrite workflow)
	// Use a no-op approver for interface compatibility
	approver := ui.NewForcedApprover(verbose)

	// Create session manager for shared session initialization logic
	sessionManager := services.NewSessionManager(
		db.NewConnector,
		fileScanner,
		fileLoader,
		logger,
	)

	// Create service (implements both Deployer and Tester interfaces)
	service := services.NewDeploymentService(
		db.NewConnector,
		approver,
		logger,
		sessionManager,
		fileScanner,
		dbManager,
	)

	// Execute tests
	ctx := context.Background()
	if err := service.ExecuteTests(ctx, config); err != nil {
		return fmt.Errorf("test execution failed: %w", err)
	}

	return nil
}
