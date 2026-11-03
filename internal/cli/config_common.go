package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// connectionFlags holds the common connection-related flag values.
type connectionFlags struct {
	connection     string
	host           string
	port           int
	username       string
	password       string // not a CLI flag; set programmatically (e.g., from wizard)
	database       string
	sslMode        string
	azure          bool
	azureTenantID  string
	azureClientID  string
	aws            bool
	awsRegion      string
	google         bool
	googleInstance string
	sslCert        string
	sslKey         string
	sslRootCert    string
}

func (f connectionFlags) granular() *db.GranularConnFlags {
	return &db.GranularConnFlags{
		Host:     f.host,
		Port:     f.port,
		Username: f.username,
		Database: f.database,
		SSLMode:  f.sslMode,
	}
}

// envConnectionSource names the environment variable the connection string
// came from, or "" when flags or pgmi.yaml supplied the connection.
func envConnectionSource(f connectionFlags) string {
	_, source := db.SelectConnectionString(f.connection, f.granular(), db.LoadFromEnvironment())
	if source == "--connection" {
		return ""
	}
	return source
}

// resolveConnectionFromFlags resolves connection configuration from flags and project config.
func resolveConnectionFromFlags(
	flags connectionFlags,
	projectCfg *config.ProjectConfig,
) (*pgmi.ConnectionConfig, string, error) {
	granularFlags := flags.granular()

	azureFlags := &db.AzureFlags{
		Enabled:  flags.azure,
		TenantID: flags.azureTenantID,
		ClientID: flags.azureClientID,
	}

	awsFlags := &db.AWSFlags{
		Enabled: flags.aws,
		Region:  flags.awsRegion,
	}

	googleFlags := &db.GoogleFlags{
		Enabled:  flags.google,
		Instance: flags.googleInstance,
	}

	certFlags := &db.CertFlags{
		SSLCert:     flags.sslCert,
		SSLKey:      flags.sslKey,
		SSLRootCert: flags.sslRootCert,
	}

	connConfig, maintenanceDB, err := db.ResolveConnectionParams(flags.connection, granularFlags, azureFlags, awsFlags, googleFlags, certFlags, db.LoadFromEnvironment(), projectCfg)
	if err != nil {
		return nil, "", err
	}

	if flags.password != "" && connConfig.Password == "" {
		connConfig.Password = flags.password
	}

	return connConfig, maintenanceDB, nil
}

// paramSources are the parameter inputs beyond pgmi.yaml. stdin is nil where
// it is not the user's to give (pgmi serve speaks MCP on it).
type paramSources struct {
	environ []string
	stdin   io.Reader
	files   []string
	pairs   []string
}

// loadMergedParameters loads and merges parameters from all sources.
// Priority (highest to lowest): --param > params files > PGMI_PARAM_* > pgmi.yaml
func loadMergedParameters(projectCfg *config.ProjectConfig, src paramSources, verbose bool) (map[string]string, error) {
	// Keys are merged lowercased: the session stores them lowercased, so
	// `Env` from pgmi.yaml and `env` from --param are one parameter and the
	// later source must win, not collide on the session table's primary key.
	parameters := make(map[string]string)
	put := func(src map[string]string) {
		for k, v := range src {
			parameters[strings.ToLower(k)] = v
		}
	}

	if projectCfg != nil {
		if err := loader.CheckCaseCollisions(projectCfg.Params); err != nil {
			return nil, fmt.Errorf("pgmi.yaml params: %w", err)
		}
		put(projectCfg.Params)
	}

	envParams, err := params.FromEnviron(src.environ)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", pgmi.ErrInvalidConfig, err)
	}
	if err := loader.CheckCaseCollisions(envParams); err != nil {
		return nil, fmt.Errorf("%s* environment variables: %w", params.EnvPrefix, err)
	}
	put(envParams)
	if verbose && len(envParams) > 0 {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Loaded %d parameter(s) from %s* environment variables\n", len(envParams), params.EnvPrefix)
	}

	if len(src.files) > 0 {
		fileParams, err := loadParamsFromFiles(filesystem.NewOSFileSystem(), src.stdin, src.files, verbose)
		if err != nil {
			return nil, err
		}
		put(fileParams)
	}

	for _, pair := range src.pairs {
		kv, err := params.ParseKeyValuePairs([]string{pair})
		if err != nil {
			return nil, fmt.Errorf("%w: invalid parameter format: %w", pgmi.ErrInvalidConfig, err)
		}
		put(kv)
	}

	if verbose && len(src.pairs) > 0 {
		fmt.Fprintf(os.Stderr, "[VERBOSE] CLI parameters override %d value(s)\n", len(src.pairs))
	}

	return parameters, nil
}

// resolveEffectiveTimeout returns the effective timeout, preferring pgmi.yaml unless the caller set one explicitly.
func resolveEffectiveTimeout(
	explicit bool,
	projectCfg *config.ProjectConfig,
	flagTimeout time.Duration,
) (time.Duration, error) {
	if projectCfg != nil && projectCfg.Timeout != "" && !explicit {
		parsed, err := time.ParseDuration(projectCfg.Timeout)
		if err != nil {
			// ErrInvalidConfig so a bad value in pgmi.yaml exits 10, like every
			// other fault in that file — an unknown field, a port that is not a
			// number, a connection block that is a scalar. This one exited 1.
			return 0, fmt.Errorf("invalid timeout in pgmi.yaml: %w: %w", err, pgmi.ErrInvalidConfig)
		}
		return parsed, nil
	}
	return flagTimeout, nil
}

// loadProjectConfig loads the project's .env and pgmi.yaml from sourcePath.
// .env is project-scoped (sourcePath/.env), never the process CWD, so the
// resolved target and credentials match the project being deployed.
// Returns nil config if pgmi.yaml does not exist (not an error).
func loadProjectConfig(sourcePath string) (*config.ProjectConfig, error) {
	envPath := filepath.Join(sourcePath, ".env")
	if err := godotenv.Load(envPath); err != nil {
		// A missing .env is normal and stays silent. A .env that exists but
		// does not parse is not: godotenv is all-or-nothing, so one stray line
		// discards the whole file and the PGPASSWORD or PGDATABASE someone put
		// there is simply absent. The deploy then fails with an unrelated
		// complaint — "database name is required" — and sends them looking for
		// a missing -d.
		//
		// This used to print only under --verbose, which is exactly when
		// nobody is looking: you reach for --verbose after the confusing
		// error, not before it.
		//
		// The underlying error is deliberately NOT included. godotenv reports
		// the text surrounding the parse failure ("... near \"<file
		// content>\""), and a .env is where PGPASSWORD lives — printing it
		// would put the credential on stderr and into CI logs. The path plus
		// the format rule is enough to act on.
		if _, statErr := os.Stat(envPath); statErr == nil {
			fmt.Fprintf(os.Stderr,
				"WARNING: %s could not be parsed, so none of its values were applied.\n"+
					"Every line must be KEY=VALUE, a # comment, or blank.\n",
				envPath)
		}
	}

	return loadProjectYAML(sourcePath)
}

// loadProjectYAML loads sourcePath/pgmi.yaml without touching the process
// environment. Returns nil config if pgmi.yaml does not exist (not an error).
func loadProjectYAML(sourcePath string) (*config.ProjectConfig, error) {
	projectCfg, err := config.Load(sourcePath)
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			return nil, nil // Config file not found is not an error
		}
		return nil, fmt.Errorf("%w: failed to load pgmi.yaml: %w", pgmi.ErrInvalidConfig, err)
	}
	return projectCfg, nil
}

// logConnectionVerbose logs connection details when verbose mode is enabled.
func logConnectionVerbose(connConfig *pgmi.ConnectionConfig, maintenanceDB string, includeMaintenanceDB bool) {
	fmt.Fprintf(os.Stderr, "[VERBOSE] Connection resolved:\n")
	fmt.Fprintf(os.Stderr, "  Host: %s\n", connConfig.Host)
	fmt.Fprintf(os.Stderr, "  Port: %d\n", connConfig.Port)
	fmt.Fprintf(os.Stderr, "  User: %s\n", connConfig.Username)
	fmt.Fprintf(os.Stderr, "  Target Database: %s\n", connConfig.Database)
	if includeMaintenanceDB {
		fmt.Fprintf(os.Stderr, "  Maintenance Database: %s\n", maintenanceDB)
	}
	fmt.Fprintf(os.Stderr, "  SSL Mode: %s\n", connConfig.SSLMode)
	if connConfig.SSLCert != "" {
		fmt.Fprintf(os.Stderr, "  SSL Cert: %s\n", connConfig.SSLCert)
	}
	if connConfig.SSLKey != "" {
		fmt.Fprintf(os.Stderr, "  SSL Key: %s\n", connConfig.SSLKey)
	}
	if connConfig.SSLRootCert != "" {
		fmt.Fprintf(os.Stderr, "  SSL Root Cert: %s\n", connConfig.SSLRootCert)
	}
	fmt.Fprintf(os.Stderr, "  Auth Method: %s\n", connConfig.AuthMethod)
}

// saveConnectionToConfig saves connection config to pgmi.yaml, merging with any existing config.
func saveConnectionToConfig(sourcePath string, connConfig *pgmi.ConnectionConfig, managementDB string) error {
	configPath := filepath.Join(sourcePath, "pgmi.yaml")

	cfg, err := config.Load(sourcePath)
	if err != nil {
		cfg = &config.ProjectConfig{}
	}

	cfg.Connection = config.ConnectionConfig{
		Host:                connConfig.Host,
		Port:                connConfig.Port,
		Username:            connConfig.Username,
		Database:            connConfig.Database,
		MaintenanceDatabase: managementDB,
		SSLMode:             connConfig.SSLMode,
		SSLCert:             connConfig.SSLCert,
		SSLKey:              connConfig.SSLKey,
		SSLRootCert:         connConfig.SSLRootCert,
		AuthMethod:          authMethodToString(connConfig.AuthMethod),
		AzureTenantID:       connConfig.AzureTenantID,
		AzureClientID:       connConfig.AzureClientID,
		AWSRegion:           connConfig.AWSRegion,
		GoogleInstance:      connConfig.GoogleInstance,
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal pgmi.yaml: %w", err)
	}

	// 0600: pgmi.yaml may accrue sensitive params via user edits; treat as
	// private by default (same convention as .pgpass). Users who need a
	// checked-in config can widen the mode themselves.
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("write pgmi.yaml: %w", err)
	}
	return nil
}

func authMethodToString(m pgmi.AuthMethod) string {
	switch m {
	case pgmi.AuthMethodAzureEntraID:
		return "azure"
	case pgmi.AuthMethodAWSIAM:
		return "aws"
	case pgmi.AuthMethodGoogleIAM:
		return "google"
	default:
		return ""
	}
}

// loadParamsFromFiles loads parameters from multiple .env files using the provided filesystem.
// "-" reads stdin, once. Later files override earlier ones.
func loadParamsFromFiles(fsProvider filesystem.FileSystemProvider, stdin io.Reader, paramsFiles []string, verbose bool) (map[string]string, error) {
	parameters := make(map[string]string)

	stdinUses := 0
	for _, f := range paramsFiles {
		if f == "-" {
			stdinUses++
		}
	}
	if stdinUses > 1 {
		return nil, fmt.Errorf("%w: --params-file - (stdin) given %d times; stdin can be read once", pgmi.ErrInvalidConfig, stdinUses)
	}
	for _, paramsFile := range paramsFiles {
		if verbose {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Loading parameters from file: %s\n", paramsFile)
		}

		fileContent, err := readParamsFile(fsProvider, stdin, paramsFile)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to read params file '%s': %w\n\nTip: Verify the path or use --param to set parameters directly:\n  pgmi deploy . --database mydb --param key=value", pgmi.ErrInvalidConfig, paramsFile, err)
		}

		fileParams, err := params.ParseEnvFile(fileContent)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to parse params file '%s': %w\n\nTip: Verify the file format (KEY=VALUE)", pgmi.ErrInvalidConfig, paramsFile, err)
		}

		if err := loader.CheckCaseCollisions(fileParams); err != nil {
			return nil, fmt.Errorf("params file '%s': %w", paramsFile, err)
		}
		for k, v := range fileParams {
			parameters[strings.ToLower(k)] = v
		}

		if verbose {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Loaded %d parameter(s) from file (total: %d)\n", len(fileParams), len(parameters))
		}
	}

	return parameters, nil
}

func readParamsFile(fsProvider filesystem.FileSystemProvider, stdin io.Reader, path string) ([]byte, error) {
	if path != "-" {
		return fsProvider.ReadFile(path)
	}
	if stdin == nil {
		return nil, errors.New("stdin is not available here")
	}
	return io.ReadAll(stdin)
}

func validateSSLMode(mode string) error {
	if mode == "" {
		return nil
	}
	if !slices.Contains(sslModes, mode) {
		return fmt.Errorf("invalid --sslmode %q (valid: %s): %w",
			mode, "disable, allow, prefer, require, verify-ca, verify-full",
			pgmi.ErrInvalidConfig)
	}
	return nil
}
