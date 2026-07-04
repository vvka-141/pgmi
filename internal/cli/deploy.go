package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

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
	Short: "Run deploy.sql against a target database",
	Long: `Run deploy.sql against a target database.

pgmi connects, loads project files into pg_temp tables, loads CLI parameters,
then executes deploy.sql. Transactions, ordering, and idempotency are decided
by deploy.sql, not by pgmi.

  pgmi deploy ./migrations -d mydb
  pgmi deploy ./migrations -d mydb --overwrite --force
  pgmi deploy ./migrations -d mydb --params-file prod.env
  pgmi deploy ./migrations -d mydb --param env=prod --param version=1.2.3

Password is never read from a flag. Use $PGPASSWORD, .pgpass, or a connection
string. Cloud auth: --azure, --aws, --google (no password needed).

Parameter precedence: --param > --params-file (later wins) > pgmi.yaml > env.

Exit codes:
  0   success
  10  invalid configuration       13  SQL execution failed
  11  connection failed           14  deploy.sql not found
  12  user denied overwrite       15  concurrent deploy in progress
  16  timeout exceeded            130 interrupted (SIGINT)`,
	Args:              RequireProjectPath,
	ValidArgsFunction: completeDirectories,
	RunE:              runDeploy,
}

type deployFlagValues struct {
	connectionFlags
	overwrite, force bool
	params           []string
	paramsFiles      []string
	timeout          time.Duration
	compat           string
	jsonOutput       bool
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
	// --host has no short form: cobra reserves -h for --help, which users
	// expect universally (kubectl, gh, git, docker). --host is typed once
	// per run, not per-invocation like in psql — the GNU help convention wins.
	deployCmd.Flags().StringVar(&deployFlags.host, "host", "",
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
		"Never block on an interactive prompt (for CI/CD; no-op when nothing would prompt)\n"+
			"With --overwrite: replaces the approval prompt with a 5-second countdown (Ctrl-C aborts)\n"+
			"Only affects confirmation dialogs, not deployment behavior")

	// Parameter flags
	deployCmd.Flags().StringArrayVar(&deployFlags.params, "param", nil,
		"Parameters as key=value pairs (can be specified multiple times)\n"+
			"Available as session variables: current_setting('pgmi.key') during deployment\n"+
			"Example: --param env=prod --param region=us-west")
	deployCmd.Flags().StringArrayVar(&deployFlags.paramsFiles, "params-file", nil,
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

	// JSON output flag
	deployCmd.Flags().BoolVar(&deployFlags.jsonOutput, "json", false,
		"Emit structured JSON to stdout after deployment")
}

// buildDeploymentConfig builds a DeploymentConfig from CLI flags and environment.
func buildDeploymentConfig(cmd *cobra.Command, sourcePath string, projectCfg *config.ProjectConfig, verbose bool) (pgmi.DeploymentConfig, error) {
	connConfig, resolvedMaintenanceDB, err := resolveConnectionFromFlags(deployFlags.connectionFlags, projectCfg)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	targetDB, err := resolveTargetDatabase(deployFlags.database, connConfig.Database, true, "deploy", verbose)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	maintenanceDB := determineMaintenanceDB(deployFlags.database, connConfig.Database, resolvedMaintenanceDB)
	connConfig.Database = targetDB

	if verbose {
		logConnectionVerbose(connConfig, maintenanceDB, true)
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
		DatabaseName:        connConfig.Database,
		MaintenanceDatabase: maintenanceDB,
		ConnectionString:    db.BuildConnectionString(connConfig),
		Overwrite:           deployFlags.overwrite,
		Force:               deployFlags.force,
		Parameters:          parameters,
		Compat:              deployFlags.compat,
		Timeout:             timeout,
		Verbose:             verbose,
		AuthMethod:          connConfig.AuthMethod,
		AzureTenantID:       connConfig.AzureTenantID,
		AzureClientID:       connConfig.AzureClientID,
		AzureClientSecret:   connConfig.AzureClientSecret,
	}, nil
}

func runDeploy(cmd *cobra.Command, args []string) error {
	sourcePath := args[0]
	verbose := getVerboseFlag(cmd)

	projectCfg, err := loadProjectConfig(sourcePath, verbose)
	if err != nil {
		return err
	}

	// Check if we need to run the connection wizard
	if needsConnectionWizard(projectCfg) && tui.IsInteractive() && !deployFlags.force {
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

	config, err := buildDeploymentConfig(cmd, sourcePath, projectCfg, verbose)
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

	// atomic.Bool because the signal goroutine and the caller observe this
	// field across a happens-before edge that is NOT provided by `Deploy`
	// returning (deploy has no synchronisation contract with sigChan). Plain
	// bool would trip `go test -race` on any integration path that exercises
	// SIGINT.
	var interrupted atomic.Bool
	go func() {
		select {
		case <-sigChan:
			fmt.Fprintln(os.Stderr, "pgmi: interrupted, cancelling deployment...")
			interrupted.Store(true)
			cancel()
		case <-ctx.Done():
			// Context cancelled (deployment completed or timeout), exit goroutine cleanly
		}
	}()

	// Set up verbose timing handler for notices
	if verbose {
		deployStart := time.Now()
		origHandler := db.NoticeHandler
		db.NoticeHandler = func(message, detail, hint string) {
			prefix := fmt.Sprintf("[%.2fs] ", time.Since(deployStart).Seconds())
			fmt.Fprintf(os.Stderr, "%s%s\n", prefix, message)
			if detail != "" {
				fmt.Fprintf(os.Stderr, "%sDETAIL: %s\n", prefix, detail)
			}
			if hint != "" {
				fmt.Fprintf(os.Stderr, "%sHINT: %s\n", prefix, hint)
			}
		}
		defer func() { db.NoticeHandler = origHandler }()
	}

	err = deployer.Deploy(ctx, config)

	// Print summary / JSON output
	if result := deployer.LastResult(); result != nil {
		if deployFlags.jsonOutput {
			printDeployJSON(result, err)
		} else {
			printDeploySummary(result, err)
		}
	}

	// If we cancelled due to SIGINT, surface context.Canceled so ExitCodeForError
	// maps to 130 (ExitInterrupted). Deployer may return nil or an unrelated wrap;
	// either way, a user Ctrl-C must not exit 0.
	if interrupted.Load() {
		if err == nil {
			return context.Canceled
		}
		if !errors.Is(err, context.Canceled) {
			return fmt.Errorf("%w: %w", context.Canceled, err)
		}
	}
	return err
}

func printDeploySummary(result *services.DeployResult, deployErr error) {
	d := fmt.Sprintf("%.2fs", result.Duration.Seconds())
	if deployErr == nil {
		parts := fmt.Sprintf("%d files loaded", result.FilesLoaded)
		if result.TestMacros > 0 {
			parts += fmt.Sprintf(", %d test macro(s) expanded", result.TestMacros)
		}
		fmt.Fprintf(os.Stderr, "%s %s in %s\n", ui.SuccessIcon(), parts, d)
	} else {
		fmt.Fprintf(os.Stderr, "%s Failed after %s — see error above\n", ui.FailIcon(), d)
	}
}

func printDeployJSON(result *services.DeployResult, deployErr error) {
	out := map[string]any{
		"status":      "success",
		"filesLoaded": result.FilesLoaded,
		"testMacros":  result.TestMacros,
		"durationMs":  result.Duration.Milliseconds(),
		"database":    result.Database,
		"exitCode":    0,
	}
	if deployErr != nil {
		out["status"] = "failed"
		out["exitCode"] = pgmi.ExitCodeForError(deployErr)
		out["error"] = deployErr.Error()
	}
	jsonBytes, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "json marshal error: %v\n", err)
		return
	}
	fmt.Println(string(jsonBytes))
}

// needsConnectionWizard checks if we have enough connection info to proceed.
// Returns true if NO connection info is available from any source.
func needsConnectionWizard(projectCfg *config.ProjectConfig) bool {
	if deployFlags.connection != "" || deployFlags.host != "" || deployFlags.database != "" {
		return false
	}

	if hasEnvConnectionSource() {
		return false
	}

	if projectCfg != nil {
		if projectCfg.Connection.Host != "" || projectCfg.Connection.Database != "" {
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
			if err := saveConnectionToConfig(sourcePath, &connResult.Config, connResult.ManagementDatabase); err != nil {
				fmt.Fprintf(os.Stderr, "WARNING: failed to save config: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "Saved to %s\n", filepath.Join(sourcePath, "pgmi.yaml"))
				offerSavePgpass(&connResult.Config)
			}
		}
	}

	return &connResult.Config, nil
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

	if cfg.Password != "" {
		deployFlags.password = cfg.Password
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
