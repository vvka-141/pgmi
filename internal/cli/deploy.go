package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/config"
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

var deployCmd = &cobra.Command{
	Use:   "deploy <project_path>",
	Short: "Execute a database deployment",
	Long: `Deploy executes a database deployment using the SQL files in the specified directory.

The deploy command:
1. Connects to PostgreSQL using the specified authentication method
2. Optionally drops and recreates the target database (with --overwrite)
3. Loads SQL files into pgmi_files temporary table
4. Loads parameters into pgmi_params temporary table
5. Executes the deploy.sql file to orchestrate the deployment

Arguments:
  project_path    Path to directory containing deploy.sql and SQL files
                  Must be a valid directory with deployment scripts

Password Authentication:
  For security, password is NOT accepted as a CLI flag. Use one of:
    1. $PGPASSWORD environment variable
    2. .pgpass file (PostgreSQL standard: chmod 600 ~/.pgpass)
    3. Connection string: postgresql://user:pass@host/db
  Never use passwords in shell commands (visible in history and process list)

Examples:
  # Basic deployment
  pgmi deploy ./migrations -d mydb

  # Deploy with overwrite (recreate database)
  pgmi deploy ./migrations -d mydb --overwrite --force

  # Deploy with parameters from file
  pgmi deploy ./migrations -d mydb --params-file prod.env

  # Deploy with multiple params files (later files override earlier ones)
  pgmi deploy ./migrations -d mydb \
    --params-file base.env \
    --params-file prod.env

  # Deploy with layered configuration (CLI overrides all files)
  pgmi deploy ./migrations -d mydb \
    --params-file base.env \
    --params-file prod.env \
    --param environment=staging \
    --param version=1.2.3`,
	Args: cobra.ExactArgs(1),
	RunE: runDeploy,
}

var (
	// Connection parameters
	deployConnection string
	deployHost       string
	deployPort       int
	deployUsername   string
	deployDatabase   string
	deploySSLMode    string

	// Azure Entra ID parameters
	deployAzureTenantID string
	deployAzureClientID string

	// Deployment options
	deployOverwrite   bool
	deployForce       bool
	deployParams      []string
	deployParamsFiles []string
	deployTimeout     time.Duration
)

func init() {
	rootCmd.AddCommand(deployCmd)

	// Connection string flag (mutually exclusive with granular flags)
	deployCmd.Flags().StringVar(&deployConnection, "connection", "",
		"PostgreSQL connection string (URI or ADO.NET format).\n"+
			"The database in the connection string is used for CREATE DATABASE operations.\n"+
			"Mutually exclusive with granular flags (--host, --port, --username).\n"+
			"Alternative: Use PGMI_CONNECTION_STRING or DATABASE_URL environment variable.\n"+
			"Example: postgresql://user:pass@localhost:5432/postgres")

	// Granular connection flags (PostgreSQL standard)
	// Precedence: flag > environment variable > default
	deployCmd.Flags().StringVar(&deployHost, "host", "",
		"PostgreSQL server host\n"+
			"Precedence: --host > $PGHOST > localhost")
	deployCmd.Flags().IntVarP(&deployPort, "port", "p", 0,
		"PostgreSQL server port\n"+
			"Precedence: --port > $PGPORT > 5432")
	deployCmd.Flags().StringVarP(&deployUsername, "username", "U", "",
		"PostgreSQL user (default: $PGUSER or current OS user)")
	deployCmd.Flags().StringVarP(&deployDatabase, "database", "d", "",
		"Target database name (optional if specified in connection string, or $PGDATABASE)\n"+
			"Examples:\n"+
			"  -d myapp                          # Deploy to 'myapp' database\n"+
			"  --connection postgresql://user@host/myapp  # Database from connection string\n"+
			"  --connection postgresql://user@host/postgres -d myapp  # Override")
	deployCmd.Flags().StringVar(&deploySSLMode, "sslmode", "",
		"SSL mode: disable|allow|prefer|require|verify-ca|verify-full\n"+
			"(default: prefer, or $PGSSLMODE)")

	// Azure Entra ID flags
	deployCmd.Flags().StringVar(&deployAzureTenantID, "azure-tenant-id", "",
		"Azure AD tenant/directory ID (overrides $AZURE_TENANT_ID)")
	deployCmd.Flags().StringVar(&deployAzureClientID, "azure-client-id", "",
		"Azure AD application/client ID (overrides $AZURE_CLIENT_ID)")

	// Deployment workflow flags
	deployCmd.Flags().BoolVar(&deployOverwrite, "overwrite", false,
		"Drop and recreate the database\n"+
			"Requires interactive confirmation unless --force is used")
	deployCmd.Flags().BoolVar(&deployForce, "force", false,
		"Skip interactive approval prompt for destructive operations\n"+
			"Only affects the confirmation dialog, not deployment behavior\n"+
			"Use with --overwrite for CI/CD pipelines")

	// Parameter flags
	deployCmd.Flags().StringSliceVar(&deployParams, "param", nil,
		"Parameters as key=value pairs (can be specified multiple times)\n"+
			"Available as session variables: current_setting('pgmi.key') during deployment\n"+
			"Example: --param env=prod --param region=us-west")
	deployCmd.Flags().StringSliceVar(&deployParamsFiles, "params-file", nil,
		"Load parameters from .env files (can be specified multiple times)\n"+
			"Later files override earlier ones, CLI --param overrides all")

	// Timeout flag - catastrophic failure protection, not normal timeout control
	deployCmd.Flags().DurationVar(&deployTimeout, "timeout", 3*time.Minute,
		"Catastrophic failure protection timeout (default 3m)\n"+
			"Prevents indefinite hangs from network issues or deadlocks\n"+
			"For query-level timeouts, use SET statement_timeout in SQL\n"+
			"Production: Set based on expected deployment duration\n"+
			"Examples: 30s, 5m, 1h30m")
}

// buildDeploymentConfig builds a DeploymentConfig from CLI flags and environment.
// This function is extracted for testability and separation of concerns.
//
// Parameters:
//   - sourcePath: Path to the deployment directory
//   - verbose: Enable verbose logging
//
// Returns:
//   - Fully configured DeploymentConfig ready for deployment
//   - Error if configuration is invalid
func buildDeploymentConfig(cmd *cobra.Command, sourcePath string, verbose bool) (pgmi.DeploymentConfig, error) {
	_ = godotenv.Load()

	projectCfg, err := config.Load(sourcePath)
	if err != nil {
		return pgmi.DeploymentConfig{}, fmt.Errorf("failed to load pgmi.yaml: %w", err)
	}

	granularFlags := &db.GranularConnFlags{
		Host:     deployHost,
		Port:     deployPort,
		Username: deployUsername,
		Database: deployDatabase,
		SSLMode:  deploySSLMode,
	}

	azureFlags := &db.AzureFlags{
		TenantID: deployAzureTenantID,
		ClientID: deployAzureClientID,
	}

	connConfig, maintenanceDB, err := resolveConnection(deployConnection, granularFlags, azureFlags, projectCfg, verbose)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	// Resolve target database: -d flag always takes precedence over connection string
	targetDB, err := resolveTargetDatabase(
		deployDatabase,
		connConfig.Database,
		true, // require database
		"deploy",
		verbose,
	)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	// Determine maintenance database for CREATE DATABASE operations
	maintenanceDB = determineMaintenanceDB(deployDatabase, connConfig.Database, maintenanceDB)

	// Update config with resolved target database
	connConfig.Database = targetDB

	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Connection resolved:\n")
		fmt.Fprintf(os.Stderr, "  Host: %s\n", connConfig.Host)
		fmt.Fprintf(os.Stderr, "  Port: %d\n", connConfig.Port)
		fmt.Fprintf(os.Stderr, "  User: %s\n", connConfig.Username)
		fmt.Fprintf(os.Stderr, "  Target Database: %s\n", connConfig.Database)
		fmt.Fprintf(os.Stderr, "  Maintenance Database: %s\n", maintenanceDB)
		fmt.Fprintf(os.Stderr, "  SSL Mode: %s\n", connConfig.SSLMode)
		fmt.Fprintf(os.Stderr, "  Auth Method: %s\n", connConfig.AuthMethod)
	}

	// Parse parameters from files (if provided)
	// Later files override earlier ones
	parameters := make(map[string]string)
	if len(deployParamsFiles) > 0 {
		// Use filesystem abstraction for reading params files
		fsProvider := filesystem.NewOSFileSystem()
		fileParams, err := loadParamsFromFiles(fsProvider, deployParamsFiles, verbose)
		if err != nil {
			return pgmi.DeploymentConfig{}, err
		}
		parameters = fileParams
	}

	// Merge pgmi.yaml params (file params < pgmi.yaml params < CLI params)
	if projectCfg != nil {
		for k, v := range projectCfg.Params {
			if _, exists := parameters[k]; !exists {
				parameters[k] = v
			}
		}
	}

	cliParams, err := params.ParseKeyValuePairs(deployParams)
	if err != nil {
		return pgmi.DeploymentConfig{}, fmt.Errorf("invalid parameter format: %w", err)
	}

	for k, v := range cliParams {
		parameters[k] = v
	}

	if verbose && len(cliParams) > 0 {
		fmt.Fprintf(os.Stderr, "[VERBOSE] CLI parameters override %d value(s)\n", len(cliParams))
	}

	// Apply timeout from pgmi.yaml if --timeout wasn't explicitly set
	timeout := deployTimeout
	if projectCfg != nil && projectCfg.Timeout != "" && !cmd.Flags().Changed("timeout") {
		parsed, parseErr := time.ParseDuration(projectCfg.Timeout)
		if parseErr != nil {
			return pgmi.DeploymentConfig{}, fmt.Errorf("invalid timeout in pgmi.yaml: %w", parseErr)
		}
		timeout = parsed
	}

	// Build connection string for deployment
	connStr := db.BuildConnectionString(connConfig)

	// Create deployment configuration
	config := pgmi.DeploymentConfig{
		SourcePath:          sourcePath,
		DatabaseName:        connConfig.Database,
		MaintenanceDatabase: maintenanceDB,
		ConnectionString:    connStr,
		Overwrite:           deployOverwrite,
		Force:               deployForce,
		Parameters:          parameters,
		Timeout:             timeout,
		Verbose:             verbose,
		AuthMethod:          connConfig.AuthMethod,
		AzureTenantID:       connConfig.AzureTenantID,
		AzureClientID:       connConfig.AzureClientID,
		AzureClientSecret:   connConfig.AzureClientSecret,
	}

	return config, nil
}

func runDeploy(cmd *cobra.Command, args []string) error {
	sourcePath := args[0]
	verbose := getVerboseFlag(cmd)

	config, err := buildDeploymentConfig(cmd, sourcePath, verbose)
	if err != nil {
		return err
	}

	// Create dependencies
	// Select approver implementation based on --force flag
	var approver pgmi.Approver
	if deployForce {
		approver = ui.NewForcedApprover(verbose)
	} else {
		approver = ui.NewInteractiveApprover(verbose)
	}
	logger := logging.NewConsoleLogger(verbose)
	fileScanner := scanner.NewScanner(checksum.New())
	fileLoader := loader.NewLoader()
	dbManager := manager.New()

	// Create session manager for shared session initialization logic
	sessionManager := services.NewSessionManager(
		db.NewConnector,
		fileScanner,
		fileLoader,
		logger,
	)

	// Create deployer with all dependencies injected
	deployer := services.NewDeploymentService(
		db.NewConnector,
		approver,
		logger,
		sessionManager,
		fileScanner,
		dbManager,
	)

	// Setup context with timeout and signal handling for graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), deployTimeout)
	defer cancel()

	// Handle interrupt signals (Ctrl+C, SIGTERM) for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Create a separate goroutine to handle signals
	go func() {
		<-sigChan
		fmt.Fprintln(os.Stderr, "\n[INTERRUPT] Received interrupt signal, cancelling deployment...")
		cancel()
	}()

	if err := deployer.Deploy(ctx, config); err != nil {
		return fmt.Errorf("deployment failed: %w", err)
	}

	return nil
}

// loadParamsFromFiles loads parameters from multiple .env files using the provided filesystem.
// Later files override earlier ones. Returns merged parameters map.
// This function is exported for testing purposes with injectable filesystem.
func loadParamsFromFiles(fsProvider filesystem.FileSystemProvider, paramsFiles []string, verbose bool) (map[string]string, error) {
	parameters := make(map[string]string)

	for _, paramsFile := range paramsFiles {
		if verbose {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Loading parameters from file: %s\n", paramsFile)
		}

		// Read file content using filesystem abstraction
		fileContent, err := fsProvider.ReadFile(paramsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read params file '%s': %w\n\nTip: Verify the path or use --param to set parameters directly:\n  pgmi deploy ./migrations --database mydb --param key=value", paramsFile, err)
		}

		// Parse .env file content
		fileParams, err := params.ParseEnvFile(fileContent)
		if err != nil {
			return nil, fmt.Errorf("failed to parse params file '%s': %w\n\nTip: Verify the file format (KEY=VALUE)", paramsFile, err)
		}

		// Merge params from this file (later files override earlier ones)
		for k, v := range fileParams {
			parameters[k] = v
		}

		if verbose {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Loaded %d parameters from file (total: %d)\n", len(fileParams), len(parameters))
		}
	}

	return parameters, nil
}
