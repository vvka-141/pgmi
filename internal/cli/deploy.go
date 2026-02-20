package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/logging"
	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/internal/tui"
	"github.com/vvka-141/pgmi/internal/tui/wizards"
	"github.com/vvka-141/pgmi/internal/ui"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var deployCmd = &cobra.Command{
	Use:   "deploy <project_path>",
	Short: "Execute a database deployment",
	Long: `Deploy executes a database deployment using the SQL files in the specified directory.

The deploy command:
1. Connects to PostgreSQL using the specified authentication method
2. Optionally drops and recreates the target database (with --overwrite)
3. Loads SQL files into pgmi_source temporary table
4. Loads parameters into pgmi_parameter temporary table
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
	Args:              RequireProjectPath,
	ValidArgsFunction: completeDirectories,
	RunE:              runDeploy,
}

type deployFlagValues struct {
	connection, host, username, database, sslMode string
	port                                          int
	azure                                         bool
	azureTenantID, azureClientID                  string
	aws                                           bool
	awsRegion                                     string
	google                                        bool
	googleInstance                                string
	sslCert, sslKey, sslRootCert                  string
	overwrite, force                              bool
	params                                        []string
	paramsFiles                                   []string
	timeout                                       time.Duration
	compat                                        string
}

var deployFlags deployFlagValues

func init() {
	rootCmd.AddCommand(deployCmd)

	// Register shell completions for flag values
	_ = deployCmd.RegisterFlagCompletionFunc("sslmode", completeSSLModes)

	// Connection string flag (mutually exclusive with granular flags)
	deployCmd.Flags().StringVar(&deployFlags.connection, "connection", "",
		"PostgreSQL connection string (URI or ADO.NET format).\n"+
			"The database in the connection string is used for CREATE DATABASE operations.\n"+
			"Mutually exclusive with granular flags (--host, --port, --username).\n"+
			"Alternative: Use PGMI_CONNECTION_STRING or DATABASE_URL environment variable.\n"+
			"Example: postgresql://user:pass@localhost:5432/postgres")

	// Granular connection flags (PostgreSQL standard)
	// Precedence: flag > environment variable > default
	deployCmd.Flags().StringVarP(&deployFlags.host, "host", "h", "",
		"PostgreSQL server host\n"+
			"Precedence: --host > $PGHOST > localhost")
	deployCmd.Flags().IntVarP(&deployFlags.port, "port", "p", 0,
		"PostgreSQL server port\n"+
			"Precedence: --port > $PGPORT > 5432")
	deployCmd.Flags().StringVarP(&deployFlags.username, "username", "U", "",
		"PostgreSQL user (default: $PGUSER or current OS user)")
	deployCmd.Flags().StringVarP(&deployFlags.database, "database", "d", "",
		"Target database name (optional if specified in connection string, or $PGDATABASE)\n"+
			"Examples:\n"+
			"  -d myapp                          # Deploy to 'myapp' database\n"+
			"  --connection postgresql://user@host/myapp  # Database from connection string\n"+
			"  --connection postgresql://user@host/postgres -d myapp  # Override")
	deployCmd.Flags().StringVar(&deployFlags.sslMode, "sslmode", "",
		"SSL mode: disable|allow|prefer|require|verify-ca|verify-full\n"+
			"(default: prefer, or $PGSSLMODE)")

	// Azure Entra ID flags
	deployCmd.Flags().BoolVar(&deployFlags.azure, "azure", false,
		"Enable Azure Entra ID authentication\n"+
			"Uses DefaultAzureCredential chain (Managed Identity, Azure CLI, etc.)")
	deployCmd.Flags().StringVar(&deployFlags.azureTenantID, "azure-tenant-id", "",
		"Azure AD tenant/directory ID (overrides $AZURE_TENANT_ID)")
	deployCmd.Flags().StringVar(&deployFlags.azureClientID, "azure-client-id", "",
		"Azure AD application/client ID (overrides $AZURE_CLIENT_ID)")

	// AWS IAM flags
	deployCmd.Flags().BoolVar(&deployFlags.aws, "aws", false,
		"Enable AWS IAM database authentication\n"+
			"Uses default AWS credential chain (env vars, config file, IAM role, etc.)")
	deployCmd.Flags().StringVar(&deployFlags.awsRegion, "aws-region", "",
		"AWS region for RDS endpoint (overrides $AWS_REGION)")

	// Google Cloud SQL IAM flags
	deployCmd.Flags().BoolVar(&deployFlags.google, "google", false,
		"Enable Google Cloud SQL IAM database authentication\n"+
			"Uses Application Default Credentials (gcloud auth, service account, etc.)")
	deployCmd.Flags().StringVar(&deployFlags.googleInstance, "google-instance", "",
		"Cloud SQL instance connection name (format: project:region:instance)\n"+
			"Required when --google is specified")

	// TLS client certificate flags
	deployCmd.Flags().StringVar(&deployFlags.sslCert, "sslcert", "",
		"Path to client SSL certificate file\n"+
			"Precedence: --sslcert > $PGSSLCERT > pgmi.yaml")
	deployCmd.Flags().StringVar(&deployFlags.sslKey, "sslkey", "",
		"Path to client SSL private key file\n"+
			"Precedence: --sslkey > $PGSSLKEY > pgmi.yaml")
	deployCmd.Flags().StringVar(&deployFlags.sslRootCert, "sslrootcert", "",
		"Path to root CA certificate for server verification\n"+
			"Precedence: --sslrootcert > $PGSSLROOTCERT > pgmi.yaml")

	// Deployment workflow flags
	deployCmd.Flags().BoolVar(&deployFlags.overwrite, "overwrite", false,
		"Drop and recreate the database\n"+
			"Requires interactive confirmation unless --force is used")
	deployCmd.Flags().BoolVar(&deployFlags.force, "force", false,
		"Skip interactive approval prompt for destructive operations\n"+
			"Only affects the confirmation dialog, not deployment behavior\n"+
			"Use with --overwrite for CI/CD pipelines")

	// Parameter flags
	deployCmd.Flags().StringSliceVar(&deployFlags.params, "param", nil,
		"Parameters as key=value pairs (can be specified multiple times)\n"+
			"Available as session variables: current_setting('pgmi.key') during deployment\n"+
			"Example: --param env=prod --param region=us-west")
	deployCmd.Flags().StringSliceVar(&deployFlags.paramsFiles, "params-file", nil,
		"Load parameters from .env files (can be specified multiple times)\n"+
			"Later files override earlier ones, CLI --param overrides all")

	// Timeout flag - catastrophic failure protection, not normal timeout control
	deployCmd.Flags().DurationVar(&deployFlags.timeout, "timeout", 3*time.Minute,
		"Catastrophic failure protection timeout (default 3m)\n"+
			"Prevents indefinite hangs from network issues or deadlocks\n"+
			"For query-level timeouts, use SET statement_timeout in SQL\n"+
			"Examples: 30s, 5m, 1h30m")

	// Compatibility level flag
	deployCmd.Flags().StringVar(&deployFlags.compat, "compat", "",
		"Compatibility level (default: latest)\n"+
			"Pin to a specific pgmi session interface version")
}

// buildDeploymentConfig builds a DeploymentConfig from CLI flags and environment.
func buildDeploymentConfig(cmd *cobra.Command, sourcePath string, verbose bool) (pgmi.DeploymentConfig, error) {
	projectCfg, err := loadProjectConfig(sourcePath)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	connFlags := connectionFlags{
		connection:     deployFlags.connection,
		host:           deployFlags.host,
		port:           deployFlags.port,
		username:       deployFlags.username,
		database:       deployFlags.database,
		sslMode:        deployFlags.sslMode,
		azure:          deployFlags.azure,
		azureTenantID:  deployFlags.azureTenantID,
		azureClientID:  deployFlags.azureClientID,
		aws:            deployFlags.aws,
		awsRegion:      deployFlags.awsRegion,
		google:         deployFlags.google,
		googleInstance: deployFlags.googleInstance,
		sslCert:        deployFlags.sslCert,
		sslKey:         deployFlags.sslKey,
		sslRootCert:    deployFlags.sslRootCert,
	}

	resolved, err := resolveConnectionFromFlags(connFlags, projectCfg, verbose)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	targetDB, err := resolveTargetDatabase(deployFlags.database, resolved.ConnConfig.Database, true, "deploy", verbose)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	maintenanceDB := determineMaintenanceDB(deployFlags.database, resolved.ConnConfig.Database, resolved.MaintenanceDB)
	resolved.ConnConfig.Database = targetDB

	if verbose {
		logConnectionVerbose(resolved.ConnConfig, maintenanceDB, true)
	}

	parameters, err := loadMergedParameters(projectCfg, deployFlags.paramsFiles, deployFlags.params, verbose)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	timeout, err := resolveEffectiveTimeout(cmd, projectCfg, deployFlags.timeout)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	return pgmi.DeploymentConfig{
		SourcePath:          sourcePath,
		DatabaseName:        resolved.ConnConfig.Database,
		MaintenanceDatabase: maintenanceDB,
		ConnectionString:    db.BuildConnectionString(resolved.ConnConfig),
		Overwrite:           deployFlags.overwrite,
		Force:               deployFlags.force,
		Parameters:          parameters,
		Compat:              deployFlags.compat,
		Timeout:             timeout,
		Verbose:             verbose,
		AuthMethod:          resolved.ConnConfig.AuthMethod,
		AzureTenantID:       resolved.ConnConfig.AzureTenantID,
		AzureClientID:       resolved.ConnConfig.AzureClientID,
		AzureClientSecret:   resolved.ConnConfig.AzureClientSecret,
	}, nil
}

func runDeploy(cmd *cobra.Command, args []string) error {
	sourcePath := args[0]
	verbose := getVerboseFlag(cmd)

	// Check if we need to run the connection wizard
	if needsConnectionWizard(sourcePath) && tui.IsInteractive() && !deployFlags.force {
		wizardConfig, err := runDeployWizard(sourcePath)
		if err != nil {
			return err
		}
		if wizardConfig == nil {
			// User cancelled
			return nil
		}
		// Merge wizard results into flags for buildDeploymentConfig
		applyWizardConfig(wizardConfig)
	}

	config, err := buildDeploymentConfig(cmd, sourcePath, verbose)
	if err != nil {
		return err
	}

	// Create dependencies
	// Select approver implementation based on --force flag
	var approver pgmi.Approver
	if deployFlags.force {
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
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	go func() {
		select {
		case <-sigChan:
			fmt.Fprintln(os.Stderr, "\n[INTERRUPT] Received interrupt signal, cancelling deployment...")
			cancel()
		case <-ctx.Done():
			// Context cancelled (deployment completed or timeout), exit goroutine cleanly
		}
	}()

	if err := deployer.Deploy(ctx, config); err != nil {
		return fmt.Errorf("deployment failed: %w", err)
	}

	return nil
}

// needsConnectionWizard checks if we have enough connection info to proceed.
// Returns true if NO connection info is available from any source.
func needsConnectionWizard(sourcePath string) bool {
	// Check CLI flags
	if deployFlags.connection != "" || deployFlags.host != "" || deployFlags.database != "" {
		return false
	}

	// Check environment variables
	if os.Getenv("DATABASE_URL") != "" || os.Getenv("PGMI_CONNECTION_STRING") != "" {
		return false
	}
	if os.Getenv("PGHOST") != "" && os.Getenv("PGDATABASE") != "" {
		return false
	}

	// Check pgmi.yaml
	cfg, err := config.Load(sourcePath)
	if err == nil && cfg != nil {
		if cfg.Connection.Host != "" || cfg.Connection.Database != "" {
			return false
		}
	}

	return true
}

// runDeployWizard runs the interactive connection wizard for deploy.
// Returns the config to use, or nil if user cancelled.
func runDeployWizard(sourcePath string) (*pgmi.ConnectionConfig, error) {
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "No database connection configured.")
	fmt.Fprintln(os.Stderr, "")

	// Run connection wizard
	connResult, err := wizards.RunConnectionWizard()
	if err != nil {
		return nil, fmt.Errorf("connection wizard failed: %w", err)
	}
	if connResult.Cancelled {
		fmt.Fprintln(os.Stderr, "Cancelled.")
		return nil, nil
	}

	// Ask if user wants to save config
	saveChoice := connResult.Tested // If tested successfully, default to asking about save

	if saveChoice && tui.IsInteractive() {
		fmt.Fprintln(os.Stderr, "")
		if tui.PromptContinue("Save this connection to pgmi.yaml for future use?") {
			if err := saveConnectionToConfig(sourcePath, &connResult.Config); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to save config: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "✓ Saved to %s\n", filepath.Join(sourcePath, "pgmi.yaml"))
				offerSavePgpass(&connResult.Config)
			}
		}
	}

	return &connResult.Config, nil
}

// saveConnectionToConfig saves connection config to pgmi.yaml.
func saveConnectionToConfig(sourcePath string, connConfig *pgmi.ConnectionConfig) error {
	configPath := filepath.Join(sourcePath, "pgmi.yaml")

	// Load existing config or create new
	cfg, err := config.Load(sourcePath)
	if err != nil {
		cfg = &config.ProjectConfig{}
	}

	// Update connection
	cfg.Connection = config.ConnectionConfig{
		Host:     connConfig.Host,
		Port:     connConfig.Port,
		Username: connConfig.Username,
		Database: connConfig.Database,
		SSLMode:  connConfig.SSLMode,
	}

	// Marshal and write
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

// applyWizardConfig applies wizard results to deploy flags.
func applyWizardConfig(cfg *pgmi.ConnectionConfig) {
	if cfg == nil {
		return
	}

	// Only set if not already set by CLI flags
	if deployFlags.host == "" {
		deployFlags.host = cfg.Host
	}
	if deployFlags.port == 0 && cfg.Port != 0 {
		deployFlags.port = cfg.Port
	}
	if deployFlags.username == "" {
		deployFlags.username = cfg.Username
	}
	if deployFlags.database == "" {
		deployFlags.database = cfg.Database
	}
	if deployFlags.sslMode == "" {
		deployFlags.sslMode = cfg.SSLMode
	}

	// Pass password via env var for this process only
	if cfg.Password != "" && os.Getenv("PGPASSWORD") == "" {
		os.Setenv("PGPASSWORD", cfg.Password)
	}

	// Cloud provider settings
	switch cfg.AuthMethod {
	case pgmi.AuthMethodAzureEntraID:
		deployFlags.azure = true
		if deployFlags.azureTenantID == "" {
			deployFlags.azureTenantID = cfg.AzureTenantID
		}
		if deployFlags.azureClientID == "" {
			deployFlags.azureClientID = cfg.AzureClientID
		}
	case pgmi.AuthMethodAWSIAM:
		deployFlags.aws = true
		if deployFlags.awsRegion == "" {
			deployFlags.awsRegion = cfg.AWSRegion
		}
	case pgmi.AuthMethodGoogleIAM:
		deployFlags.google = true
		if deployFlags.googleInstance == "" {
			deployFlags.googleInstance = cfg.GoogleInstance
		}
	}
}

