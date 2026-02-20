package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// connectionFlags holds the common connection-related flag values.
type connectionFlags struct {
	connection     string
	host           string
	port           int
	username       string
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

// resolvedConnection holds the resolved connection configuration.
type resolvedConnection struct {
	ConnConfig    *pgmi.ConnectionConfig
	MaintenanceDB string
	ConnStr       string
}

// resolveConnectionFromFlags resolves connection configuration from flags and project config.
func resolveConnectionFromFlags(
	flags connectionFlags,
	projectCfg *config.ProjectConfig,
	verbose bool,
) (*resolvedConnection, error) {
	granularFlags := &db.GranularConnFlags{
		Host:     flags.host,
		Port:     flags.port,
		Username: flags.username,
		Database: flags.database,
		SSLMode:  flags.sslMode,
	}

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

	connConfig, maintenanceDB, err := resolveConnection(flags.connection, granularFlags, azureFlags, awsFlags, googleFlags, certFlags, projectCfg, verbose)
	if err != nil {
		return nil, err
	}

	return &resolvedConnection{
		ConnConfig:    connConfig,
		MaintenanceDB: maintenanceDB,
		ConnStr:       db.BuildConnectionString(connConfig),
	}, nil
}

// loadMergedParameters loads and merges parameters from all sources.
// Priority (highest to lowest): CLI params > params files > pgmi.yaml
func loadMergedParameters(
	projectCfg *config.ProjectConfig,
	paramsFiles []string,
	cliParamPairs []string,
	verbose bool,
) (map[string]string, error) {
	parameters := make(map[string]string)

	// First: pgmi.yaml params (lowest priority)
	if projectCfg != nil {
		for k, v := range projectCfg.Params {
			parameters[k] = v
		}
	}

	// Second: params files (override pgmi.yaml)
	if len(paramsFiles) > 0 {
		fsProvider := filesystem.NewOSFileSystem()
		fileParams, err := loadParamsFromFiles(fsProvider, paramsFiles, verbose)
		if err != nil {
			return nil, err
		}
		for k, v := range fileParams {
			parameters[k] = v
		}
	}

	// Third: CLI params (highest priority)
	cliParams, err := params.ParseKeyValuePairs(cliParamPairs)
	if err != nil {
		return nil, fmt.Errorf("invalid parameter format: %w", err)
	}
	for k, v := range cliParams {
		parameters[k] = v
	}

	if verbose && len(cliParams) > 0 {
		fmt.Fprintf(os.Stderr, "[VERBOSE] CLI parameters override %d value(s)\n", len(cliParams))
	}

	return parameters, nil
}

// resolveEffectiveTimeout returns the effective timeout, preferring pgmi.yaml if flag wasn't set.
func resolveEffectiveTimeout(
	cmd *cobra.Command,
	projectCfg *config.ProjectConfig,
	flagTimeout time.Duration,
) (time.Duration, error) {
	if projectCfg != nil && projectCfg.Timeout != "" && !cmd.Flags().Changed("timeout") {
		parsed, err := time.ParseDuration(projectCfg.Timeout)
		if err != nil {
			return 0, fmt.Errorf("invalid timeout in pgmi.yaml: %w", err)
		}
		return parsed, nil
	}
	return flagTimeout, nil
}

// loadProjectConfig loads godotenv and project configuration.
// Returns nil config if pgmi.yaml does not exist (not an error).
func loadProjectConfig(sourcePath string) (*config.ProjectConfig, error) {
	_ = godotenv.Load()

	projectCfg, err := config.Load(sourcePath)
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			return nil, nil // Config file not found is not an error
		}
		return nil, fmt.Errorf("failed to load pgmi.yaml: %w", err)
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
		Host:               connConfig.Host,
		Port:               connConfig.Port,
		Username:           connConfig.Username,
		Database:           connConfig.Database,
		ManagementDatabase: managementDB,
		SSLMode:            connConfig.SSLMode,
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

// loadParamsFromFiles loads parameters from multiple .env files using the provided filesystem.
// Later files override earlier ones. Returns merged parameters map.
func loadParamsFromFiles(fsProvider filesystem.FileSystemProvider, paramsFiles []string, verbose bool) (map[string]string, error) {
	parameters := make(map[string]string)

	for _, paramsFile := range paramsFiles {
		if verbose {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Loading parameters from file: %s\n", paramsFile)
		}

		fileContent, err := fsProvider.ReadFile(paramsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read params file '%s': %w\n\nTip: Verify the path or use --param to set parameters directly:\n  pgmi deploy ./migrations --database mydb --param key=value", paramsFile, err)
		}

		fileParams, err := params.ParseEnvFile(fileContent)
		if err != nil {
			return nil, fmt.Errorf("failed to parse params file '%s': %w\n\nTip: Verify the file format (KEY=VALUE)", paramsFile, err)
		}

		for k, v := range fileParams {
			parameters[k] = v
		}

		if verbose {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Loaded %d parameters from file (total: %d)\n", len(fileParams), len(parameters))
		}
	}

	return parameters, nil
}
