package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/logging"
	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/internal/ui"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var testCmd = &cobra.Command{
	Use:   "test <project_path>",
	Short: "Execute database tests",
	Long: `Test executes database unit tests using the SQL files in the specified directory.

The test command:
1. Connects to the target database (must already exist)
2. Loads SQL files into pg_temp.pgmi_source
3. Loads test files into pg_temp.pgmi_test_plan
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
  pgmi test ./myapp -d test_db --param test_user_id=123

  # Run tests with Azure Entra ID (Managed Identity)
  pgmi test ./myapp -d test_db --azure

  # Run tests with Azure Entra ID (Service Principal)
  pgmi test ./myapp -d test_db --azure-tenant-id $AZURE_TENANT_ID --azure-client-id $AZURE_CLIENT_ID`,
	Args: cobra.ExactArgs(1),
	RunE: runTest,
}

type testFlagValues struct {
	connection, host, username, database, sslMode string
	port                                          int
	azure                                         bool
	azureTenantID, azureClientID                  string
	sslCert, sslKey, sslRootCert                  string
	filter                                        string
	list                                          bool
	params                                        []string
	paramsFiles                                   []string
	timeout                                       time.Duration
}

var testFlags testFlagValues

func init() {
	rootCmd.AddCommand(testCmd)

	// Connection string flag (mutually exclusive with granular flags)
	testCmd.Flags().StringVar(&testFlags.connection, "connection", "",
		"PostgreSQL connection string (URI or ADO.NET format).\n"+
			"Mutually exclusive with granular flags (--host, --port, --username).\n"+
			"Alternative: Use PGMI_CONNECTION_STRING or DATABASE_URL environment variable.\n"+
			"Example: postgresql://user:pass@localhost:5432/test_db")

	testCmd.Flags().StringVarP(&testFlags.host, "host", "h", "",
		"PostgreSQL server host\n"+
			"Precedence: --host > $PGHOST > localhost")
	testCmd.Flags().IntVarP(&testFlags.port, "port", "p", 0,
		"PostgreSQL server port\n"+
			"Precedence: --port > $PGPORT > 5432")
	testCmd.Flags().StringVarP(&testFlags.username, "username", "U", "",
		"PostgreSQL user (default: $PGUSER or current OS user)")
	testCmd.Flags().StringVarP(&testFlags.database, "database", "d", "",
		"Target database name (optional if specified in connection string, or $PGDATABASE)")
	testCmd.Flags().StringVar(&testFlags.sslMode, "sslmode", "",
		"SSL mode: disable|allow|prefer|require|verify-ca|verify-full\n"+
			"(default: prefer, or $PGSSLMODE)")

	testCmd.Flags().BoolVar(&testFlags.azure, "azure", false,
		"Enable Azure Entra ID authentication\n"+
			"Uses DefaultAzureCredential chain (Managed Identity, Azure CLI, etc.)")
	testCmd.Flags().StringVar(&testFlags.azureTenantID, "azure-tenant-id", "",
		"Azure AD tenant/directory ID (overrides $AZURE_TENANT_ID)")
	testCmd.Flags().StringVar(&testFlags.azureClientID, "azure-client-id", "",
		"Azure AD application/client ID (overrides $AZURE_CLIENT_ID)")

	// TLS client certificate flags
	testCmd.Flags().StringVar(&testFlags.sslCert, "sslcert", "",
		"Path to client SSL certificate file\n"+
			"Precedence: --sslcert > $PGSSLCERT > pgmi.yaml")
	testCmd.Flags().StringVar(&testFlags.sslKey, "sslkey", "",
		"Path to client SSL private key file\n"+
			"Precedence: --sslkey > $PGSSLKEY > pgmi.yaml")
	testCmd.Flags().StringVar(&testFlags.sslRootCert, "sslrootcert", "",
		"Path to root CA certificate for server verification\n"+
			"Precedence: --sslrootcert > $PGSSLROOTCERT > pgmi.yaml")

	testCmd.Flags().StringVar(&testFlags.filter, "filter", ".*", "POSIX regex pattern to filter tests (default: \".*\" matches all)\n"+
		"Examples:\n"+
		"  --filter auth           # Tests with 'auth' in the path\n"+
		"  --filter pre-deployment # Tests in pre-deployment directories\n"+
		"  --filter 001            # Tests matching '001'")
	testCmd.Flags().BoolVar(&testFlags.list, "list", false, "List tests without executing them (dry-run mode)")
	testCmd.Flags().StringSliceVar(&testFlags.params, "param", nil, "Parameters as key=value pairs (for parameterized tests)")
	testCmd.Flags().StringSliceVar(&testFlags.paramsFiles, "params-file", nil,
		"Load parameters from .env files (can be specified multiple times)\n"+
			"Later files override earlier ones, CLI --param overrides all")

	testCmd.Flags().DurationVar(&testFlags.timeout, "timeout", 3*time.Minute,
		"Catastrophic failure protection timeout (default 3m)\n"+
			"Prevents indefinite hangs from network issues or deadlocks\n"+
			"For query-level timeouts, use SET statement_timeout in SQL\n"+
			"Examples: 30s, 5m, 1h30m")
}

// buildTestConfig builds a TestConfig from CLI flags and environment.
func buildTestConfig(cmd *cobra.Command, sourcePath string, verbose bool) (pgmi.TestConfig, error) {
	projectCfg, err := loadProjectConfig(sourcePath)
	if err != nil {
		return pgmi.TestConfig{}, err
	}

	connFlags := connectionFlags{
		connection:    testFlags.connection,
		host:          testFlags.host,
		port:          testFlags.port,
		username:      testFlags.username,
		database:      testFlags.database,
		sslMode:       testFlags.sslMode,
		azure:         testFlags.azure,
		azureTenantID: testFlags.azureTenantID,
		azureClientID: testFlags.azureClientID,
		sslCert:       testFlags.sslCert,
		sslKey:        testFlags.sslKey,
		sslRootCert:   testFlags.sslRootCert,
	}

	resolved, err := resolveConnectionFromFlags(connFlags, projectCfg, verbose)
	if err != nil {
		return pgmi.TestConfig{}, err
	}

	targetDB, err := resolveTargetDatabase(testFlags.database, resolved.ConnConfig.Database, true, "test", verbose)
	if err != nil {
		return pgmi.TestConfig{}, err
	}
	resolved.ConnConfig.Database = targetDB

	if verbose {
		logConnectionVerbose(resolved.ConnConfig, "", false)
		fmt.Fprintf(os.Stderr, "[VERBOSE] Source path: %s\n", sourcePath)
		fmt.Fprintf(os.Stderr, "[VERBOSE] Filter pattern: %s\n", testFlags.filter)
	}

	parameters, err := loadMergedParameters(projectCfg, testFlags.paramsFiles, testFlags.params, verbose)
	if err != nil {
		return pgmi.TestConfig{}, err
	}

	timeout, err := resolveEffectiveTimeout(cmd, projectCfg, testFlags.timeout)
	if err != nil {
		return pgmi.TestConfig{}, err
	}

	return pgmi.TestConfig{
		SourcePath:        sourcePath,
		DatabaseName:      resolved.ConnConfig.Database,
		ConnectionString:  db.BuildConnectionString(resolved.ConnConfig),
		Timeout:           timeout,
		FilterPattern:     testFlags.filter,
		ListOnly:          testFlags.list,
		Parameters:        parameters,
		Verbose:           verbose,
		AuthMethod:        resolved.ConnConfig.AuthMethod,
		AzureTenantID:     resolved.ConnConfig.AzureTenantID,
		AzureClientID:     resolved.ConnConfig.AzureClientID,
		AzureClientSecret: resolved.ConnConfig.AzureClientSecret,
	}, nil
}

func runTest(cmd *cobra.Command, args []string) error {
	sourcePath := args[0]
	verbose := getVerboseFlag(cmd)

	// Build test configuration
	config, err := buildTestConfig(cmd, sourcePath, verbose)
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

	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	go func() {
		select {
		case <-sigChan:
			fmt.Fprintln(os.Stderr, "\n[INTERRUPT] Received interrupt signal, cancelling test execution...")
			cancel()
		case <-ctx.Done():
			// Context cancelled (test completed or timeout), exit goroutine cleanly
		}
	}()

	if err := service.ExecuteTests(ctx, config); err != nil {
		return fmt.Errorf("test execution failed: %w", err)
	}

	return nil
}
