package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/contract"
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

  pgmi deploy . -d mydb
  pgmi deploy . -d mydb --overwrite --force
  pgmi deploy . -d mydb --params-file prod.env
  pgmi deploy . -d mydb --param env=prod --param version=1.2.3
  ./fetch-secrets | pgmi deploy . -d mydb --params-file -

Password is never read from a flag. Use $PGPASSWORD, .pgpass, or a connection
string. Cloud auth: --azure, --aws, --google (no password needed).

Parameter precedence: --param > --params-file (later wins) > PGMI_PARAM_*
environment variables > pgmi.yaml. PGMI_PARAM_DB_PASSWORD=x becomes
current_setting('pgmi.db_password'); other environment variables are not read.
Secrets go in the environment or a params file, never in --param.

Exit codes:
  0   success
  10  invalid configuration       13  SQL execution failed
  11  connection failed           14  deploy.sql not found
  12  user denied overwrite       15  concurrent deploy in progress
  16  timeout exceeded            130 interrupted (SIGINT)
A --timeout that runs out while connecting is 11: the server was not reached.`,
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

// Seams for the connection-wizard branch. tui.IsInteractive() is hard-false
// under `go test`, so without these the branch — and the exit-12 a cancelled
// wizard produces — cannot be reached from a test at all.
var (
	isInteractive       = tui.IsInteractive
	canPrompt           = tui.CanPrompt
	runConnectionWizard = runDeployWizard
)

func init() {
	rootCmd.AddCommand(deployCmd)

	// Register shell completions for flag values
	_ = deployCmd.RegisterFlagCompletionFunc("sslmode", completeSSLModes)

	// Connection string flag (mutually exclusive with granular flags)
	deployCmd.Flags().StringVar(&deployFlags.connection, "connection", "",
		"PostgreSQL connection string: URI, libpq keyword/value (host=... dbname=...), or ADO.NET.\n"+
			"Its database is the maintenance database for CREATE DATABASE, and also the\n"+
			"deploy target unless -d says otherwise. It outranks pgmi.yaml's\n"+
			"connection.database, so a string ending in /postgres deploys into postgres.\n"+
			"Fields it omits come from $PGHOST, $PGPORT, $PGDATABASE, $PGUSER, $PGAPPNAME\n"+
			"and $PGCONNECT_TIMEOUT, as in libpq, then from the defaults (localhost:5432/postgres).\n"+
			"service= and $PGSERVICE are refused: pgmi reads no service files.\n"+
			"Mutually exclusive with granular flags (--host, --port, --username).\n"+
			"Alternative: Use PGMI_CONNECTION_STRING or DATABASE_URL environment variable.\n"+
			"Example: postgresql://user:pass@localhost:5432/postgres")

	// Granular connection flags (PostgreSQL standard)
	// Precedence: flag > environment variable > pgmi.yaml > default
	deployCmd.Flags().StringVarP(&deployFlags.host, "host", "h", "",
		"PostgreSQL server host\n"+
			"Precedence: --host > $PGHOST > pgmi.yaml > localhost")
	deployCmd.Flags().IntVarP(&deployFlags.port, "port", "p", 0,
		"PostgreSQL server port\n"+
			"Precedence: --port > $PGPORT > pgmi.yaml > 5432")
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
			"Requires interactive confirmation unless --force is used\n"+
			"Without a terminal and without --force, pgmi refuses (exit 12) rather than prompting")
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
			"Use - to read stdin (once). Later files override earlier ones, CLI --param overrides all")

	// Timeout flag - catastrophic failure protection, not normal timeout control
	deployCmd.Flags().DurationVar(&deployFlags.timeout, "timeout", 3*time.Minute,
		"Catastrophic failure protection timeout (default 3m)\n"+
			"Prevents indefinite hangs from network issues or deadlocks\n"+
			"Use 0 to disable the limit\n"+
			"For query-level timeouts, use SET statement_timeout in SQL\n"+
			"Examples: 30s, 5m, 1h30m, 0")

	// Compatibility level flag
	deployCmd.Flags().StringVar(&deployFlags.compat, "compat", "",
		"Compatibility level (default: latest)\n"+
			"Pin to a specific pgmi session interface version")

	// JSON output flag
	deployCmd.Flags().BoolVar(&deployFlags.jsonOutput, "json", false,
		"Emit structured JSON to stdout after deployment")
}

// timeoutExceeded names --timeout when the deploy's own deadline ended it. A
// driver's connect_timeout also reads as a deadline, but not as ctx's.
func timeoutExceeded(ctx context.Context, err error, timeout time.Duration) error {
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w\nthe deploy exceeded --timeout %s; raise it, or pass --timeout 0 for no limit", err, timeout)
}

func deadlineContext(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d == 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, d)
}

// deployRequest is everything a deploy caller supplies on top of pgmi.yaml and
// the environment. The CLI fills it from flags, pgmi serve from tool arguments;
// both resolve through resolveDeploymentConfig so they cannot drift.
type deployRequest struct {
	conn                connectionFlags
	overwrite, force    bool
	params              []string
	paramsFiles         []string
	environ             []string
	stdin               io.Reader
	timeout             time.Duration
	timeoutSet          bool
	maintenanceDatabase string
	compat              string
	verbose             bool
	announceEnvSource   bool
}

// buildDeploymentConfig builds a DeploymentConfig from CLI flags and environment.
func buildDeploymentConfig(cmd *cobra.Command, sourcePath string, projectCfg *config.ProjectConfig, verbose bool) (pgmi.DeploymentConfig, error) {
	return resolveDeploymentConfig(sourcePath, projectCfg, deployRequest{
		conn:              deployFlags.connectionFlags,
		overwrite:         deployFlags.overwrite,
		force:             deployFlags.force,
		params:            deployFlags.params,
		paramsFiles:       deployFlags.paramsFiles,
		environ:           os.Environ(),
		stdin:             os.Stdin,
		timeout:           deployFlags.timeout,
		timeoutSet:        cmd.Flags().Changed("timeout"),
		compat:            deployFlags.compat,
		verbose:           verbose,
		announceEnvSource: !deployFlags.jsonOutput,
	})
}

func resolveDeploymentConfig(sourcePath string, projectCfg *config.ProjectConfig, req deployRequest) (pgmi.DeploymentConfig, error) {
	if err := validateSSLMode(req.conn.sslMode); err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	connConfig, resolvedMaintenanceDB, err := resolveConnectionFromFlags(req.conn, projectCfg)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}
	if connConfig.SSLMode != "" && !slices.Contains(sslModes, connConfig.SSLMode) {
		return pgmi.DeploymentConfig{}, fmt.Errorf("%w: invalid sslmode %q in the connection (valid: %s)",
			pgmi.ErrInvalidConfig, connConfig.SSLMode, strings.Join(sslModes, ", "))
	}

	targetDB, err := resolveTargetDatabase(req.conn.database, connConfig.Database, req.verbose)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	maintenanceDB := determineMaintenanceDB(req.conn.database, connConfig.Database, resolvedMaintenanceDB)
	if req.maintenanceDatabase != "" {
		maintenanceDB = req.maintenanceDatabase
	}
	connConfig.Database = targetDB

	if source := envConnectionSource(req.conn); source != "" && req.announceEnvSource {
		fmt.Fprintf(os.Stderr, "connection: from %s (%s)\n", source, db.DialTargets(connConfig))
	}

	if req.verbose {
		logConnectionVerbose(connConfig, maintenanceDB, true)
	}

	parameters, err := loadMergedParameters(projectCfg, paramSources{environ: req.environ, stdin: req.stdin, files: req.paramsFiles, pairs: req.params}, req.verbose)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	timeout, err := resolveEffectiveTimeout(req.timeoutSet, projectCfg, req.timeout)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	return pgmi.DeploymentConfig{
		SourcePath:          sourcePath,
		DatabaseName:        connConfig.Database,
		MaintenanceDatabase: maintenanceDB,
		ConnectionString:    db.BuildConnectionString(connConfig),
		Overwrite:           req.overwrite,
		Force:               req.force,
		Parameters:          parameters,
		Compat:              req.compat,
		Timeout:             timeout,
		Verbose:             req.verbose,
		AuthMethod:          connConfig.AuthMethod,
		AzureTenantID:       connConfig.AzureTenantID,
		AzureClientID:       connConfig.AzureClientID,
		AzureClientSecret:   connConfig.AzureClientSecret,
		AWSRegion:           connConfig.AWSRegion,
		GoogleInstance:      connConfig.GoogleInstance,
	}, nil
}

func runDeploy(cmd *cobra.Command, args []string) (err error) {
	sourcePath := args[0]
	verbose := getVerboseFlag(cmd)

	// --json is a contract: emit the failure envelope even when the error
	// occurs before Deploy() runs (bad connection string, bad params file)
	jsonEmitted := false
	prov := deployProvenance{startedAt: time.Now(), compat: deployFlags.compat}
	defer func() {
		if err != nil && deployFlags.jsonOutput && !jsonEmitted {
			printDeployJSON(nil, err, prov)
		}
	}()

	projectCfg, err := loadProjectConfig(sourcePath)
	if err != nil {
		return err
	}

	// Check if we need to run the connection wizard
	if needsConnectionWizard(projectCfg) && isInteractive() && !deployFlags.force {
		wizardConfig, err := runConnectionWizard(sourcePath)
		if err != nil {
			return err
		}
		if wizardConfig == nil {
			// A cancelled wizard deployed nothing; exiting 0 would report
			// success to a caller that got no deployment.
			return fmt.Errorf("%w: connection setup cancelled", pgmi.ErrApprovalDenied)
		}
		// Merge wizard results into flags for buildDeploymentConfig
		applyWizardConfig(wizardConfig)
	}

	if source := envConnectionSource(deployFlags.connectionFlags); source != "" {
		defer func() {
			if errors.Is(err, pgmi.ErrConnectionFailed) || errors.Is(err, pgmi.ErrInvalidConfig) {
				err = fmt.Errorf("%w\n(connection from %s)", err, source)
			}
		}()
	}

	config, err := buildDeploymentConfig(cmd, sourcePath, projectCfg, verbose)
	if err != nil {
		return err
	}
	prov.target = targetOf(config)

	approver := selectApprover(deployFlags.force, canPrompt(), verbose)

	logger := logging.NewConsoleLogger(verbose)
	fileScanner := scanner.NewScanner(checksum.New())
	fileLoader := loader.NewLoader()
	dbManager := manager.New()

	// Create session manager for shared session initialization logic
	pgmiVersion, _, _ := resolveVersionInfo()
	sessionManager := services.NewSessionManager(
		db.NewConnector,
		fileScanner,
		fileLoader,
		logger,
		pgmiVersion,
	)

	// Create deployer with all dependencies injected
	deployer := services.NewDeploymentService(
		db.NewConnector,
		approver,
		logger,
		sessionManager,
		dbManager,
	)

	ctx, cancel := deadlineContext(context.Background(), config.Timeout)
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
			fmt.Fprintln(os.Stderr, "pgmi: interrupted, cancelling deployment... (again to quit at once)")
			interrupted.Store(true)
			cancel()
			<-sigChan
			fmt.Fprintln(os.Stderr, "pgmi: interrupted again, quitting")
			os.Exit(pgmi.ExitInterrupted)
		case <-ctx.Done():
			// Context cancelled (deployment completed or timeout), exit goroutine cleanly
		}
	}()

	// Set up verbose timing handler for notices
	if verbose {
		deployStart := time.Now()
		origHandler := db.NoticeHandler
		db.NoticeHandler = func(severity, message, detail, hint string) {
			prefix := fmt.Sprintf("[%.2fs] ", time.Since(deployStart).Seconds())
			db.WriteNotice(os.Stderr, prefix, severity, message, detail, hint)
		}
		defer func() { db.NoticeHandler = origHandler }()
	}

	err = timeoutExceeded(ctx, deployer.Deploy(ctx, config), config.Timeout)
	err, jsonEmitted = finishDeploy(deployer.LastResult(), err,
		interrupted.Load(), deployFlags.jsonOutput, prov)
	if verbose && !deployFlags.jsonOutput && deployer.LastResult() != nil {
		printIgnoredPaths(deployer.LastResult())
	}
	return err
}

// finishDeploy resolves the error the process will exit on, then reports the
// run. The order is the point: printDeployJSON derives "status" and "exitCode"
// from the error it is handed, so reporting before the interrupt is folded in
// publishes {"status":"success","exitCode":0} for a run that exits 130.
//
// On SIGINT it surfaces context.Canceled so ExitCodeForError maps to 130. The
// deployer may return nil or an unrelated wrap; either way a user Ctrl-C must
// not exit 0.
func finishDeploy(result *services.DeployResult, deployErr error, interrupted, jsonOutput bool, prov deployProvenance) (err error, jsonEmitted bool) {
	err = deployErr
	if interrupted {
		switch {
		case err == nil:
			err = context.Canceled
		case !errors.Is(err, context.Canceled):
			err = fmt.Errorf("%w: %w", context.Canceled, err)
		}
	}

	if result != nil {
		if jsonOutput {
			printDeployJSON(result, err, prov)
			jsonEmitted = true
		} else {
			printDeploySummary(result, err)
		}
	}
	return err, jsonEmitted
}

// printIgnoredPaths lists what discovery left out and which rule did it, so a
// file missing from the session is explained rather than guessed at.
func printIgnoredPaths(result *services.DeployResult) {
	for _, ig := range result.Ignored {
		fmt.Fprintf(os.Stderr, "  ignored %s (%s)\n", ig.Path, ig.Rule)
	}
}

// printDeploySummary names the target database on every run, not only when it
// had to be created. "Database %q does not exist; creating" used to be the only
// line mentioning a database, so a deploy that landed somewhere the user did not
// intend — a connection string ending in /postgres outranking pgmi.yaml — said
// nothing to contradict the wrong mental model.
func printDeploySummary(result *services.DeployResult, deployErr error) {
	d := fmt.Sprintf("%.2fs", result.Duration.Seconds())
	target := result.Database
	if target == "" {
		target = "?"
	}
	if deployErr == nil {
		loaded := fmt.Sprintf("%d files loaded", result.FilesLoaded)
		if n := len(result.Ignored); n > 0 {
			loaded += fmt.Sprintf(", %d paths ignored", n)
		}
		fmt.Fprintf(os.Stderr, "%s %s: %s in %s\n", ui.SuccessIcon(), target, loaded, d)
	} else {
		msg := fmt.Sprintf("failed after %s", d)
		if result.ExecutionMode == "psql" {
			msg += fmt.Sprintf(" (unit %d of %d failed; %d earlier unit(s) already committed)",
				result.FailedUnit, result.ExecutionUnits, result.UnitsCommitted)
		}
		fmt.Fprintf(os.Stderr, "%s %s: %s\n", ui.FailIcon(), target, msg)
	}
}

func printDeployJSON(result *services.DeployResult, deployErr error, prov deployProvenance) {
	jsonBytes, err := json.MarshalIndent(deployResultFields(result, deployErr, prov), "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "json marshal error: %v\n", err)
		return
	}
	fmt.Println(string(jsonBytes))
}

// deployJSONSchemaVersion versions the shape of the --json record itself.
// Bump it when a key is renamed or removed, not when one is added.
const deployJSONSchemaVersion = 1

// deployTarget is where a deploy went. It never carries the user, password or
// token: the record is meant to be kept as CI output.
type deployTarget struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
}

// deployProvenance is what identifies a run in the --json record. A failed
// deploy rolls back, so that record is often the only one that survives.
type deployProvenance struct {
	startedAt time.Time
	compat    string
	target    *deployTarget
}

func targetOf(cfg pgmi.DeploymentConfig) *deployTarget {
	c, err := db.ParseConnectionString(cfg.ConnectionString)
	if err != nil {
		return nil
	}
	return &deployTarget{Host: c.Host, Port: c.Port, Database: cfg.DatabaseName}
}

// deployResultFields is the machine-readable deploy result, shared by
// `deploy --json` and the MCP deploy tool so the two cannot drift.
// Provenance keys are always present; a fact the run never reached is null.
func deployResultFields(result *services.DeployResult, deployErr error, prov deployProvenance) map[string]any {
	pgmiVersion, _, _ := resolveVersionInfo()
	out := map[string]any{
		"status":          "success",
		"exitCode":        0,
		"schemaVersion":   deployJSONSchemaVersion,
		"pgmiVersion":     pgmiVersion,
		"contractVersion": nil,
		"serverVersion":   nil,
		"target":          nil,
		"startedAt":       prov.startedAt.UTC().Format(time.RFC3339Nano),
		"finishedAt":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, v, err := contract.Load(prov.compat); err == nil {
		out["contractVersion"] = string(v)
	}
	if prov.target != nil {
		out["target"] = prov.target
	}
	if result != nil && result.ServerVersion != "" {
		out["serverVersion"] = result.ServerVersion
	}
	if result != nil {
		out["filesLoaded"] = result.FilesLoaded
		out["pathsIgnored"] = len(result.Ignored)
		ignored := make([]map[string]string, 0, len(result.Ignored))
		for _, ig := range result.Ignored {
			ignored = append(ignored, map[string]string{"path": ig.Path, "rule": ig.Rule})
		}
		out["ignoredPaths"] = ignored
		out["testMacros"] = result.TestMacros
		out["durationMs"] = result.Duration.Milliseconds()
		out["database"] = result.Database
		out["created"] = result.Created
		if result.ExecutionUnits > 0 {
			out["executionUnits"] = result.ExecutionUnits
			out["unitsCommitted"] = result.UnitsCommitted
		}
		if result.ExecutionMode != "" {
			out["executionMode"] = result.ExecutionMode
		}
	}
	if d := pgmi.NewErrorDetail(deployErr); d != nil {
		out["status"] = "failed"
		out["exitCode"] = d.ExitCode
		out["error"] = d.Message
		for k, v := range map[string]string{
			"sqlstate":   d.SQLState,
			"detail":     d.Detail,
			"hint":       d.Hint,
			"where":      d.Where,
			"failedFile": d.FailedFile,
			"phase":      d.Phase,
			"script":     d.Script,
			"sourceLine": d.SourceLine,
		} {
			if v != "" {
				out[k] = v
			}
		}
		if len(d.FailedFileCandidates) > 0 {
			out["failedFileCandidates"] = d.FailedFileCandidates
		}
		if d.Line > 0 {
			out["line"] = d.Line
			out["column"] = d.Column
			out["scriptExpanded"] = d.ScriptExpanded
		}
	}
	return out
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

// selectApprover picks the gate that stands between --overwrite and DROP
// DATABASE. --force bypasses confirmation. Without a terminal there is nobody
// to confirm, so refusing beats prompting into a pipe that never answers —
// that hung for the whole of --timeout and then reported exit 16, "operation
// exceeded --timeout", for what was a missing --force.
func selectApprover(force, interactive, verbose bool) pgmi.Approver {
	switch {
	case force:
		return ui.NewForcedApprover(verbose)
	case !interactive:
		return ui.NewNonInteractiveApprover()
	default:
		return ui.NewInteractiveApprover(verbose)
	}
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
			if err := saveConnectionToConfig(sourcePath, &connResult.Config, connResult.MaintenanceDatabase); err != nil {
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
