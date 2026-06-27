package cli

import (
	"errors"
	"fmt"
	"maps"
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

// resolveConnectionFromFlags resolves connection configuration from flags and project config.
func resolveConnectionFromFlags(
	flags connectionFlags,
	projectCfg *config.ProjectConfig,
) (*pgmi.ConnectionConfig, string, error) {
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

	connConfig, maintenanceDB, err := resolveConnection(flags.connection, granularFlags, azureFlags, awsFlags, googleFlags, certFlags, projectCfg)
	if err != nil {
		return nil, "", err
	}

	if flags.password != "" && connConfig.Password == "" {
		connConfig.Password = flags.password
	}

	return connConfig, maintenanceDB, nil
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

	if projectCfg != nil {
		maps.Copy(parameters, projectCfg.Params)
	}

	if len(paramsFiles) > 0 {
		fsProvider := filesystem.NewOSFileSystem()
		fileParams, err := loadParamsFromFiles(fsProvider, paramsFiles, verbose)
		if err != nil {
			return nil, err
		}
		maps.Copy(parameters, fileParams)
	}

	cliParams, err := params.ParseKeyValuePairs(cliParamPairs)
	if err != nil {
		return nil, fmt.Errorf("invalid parameter format: %w", err)
	}
	maps.Copy(parameters, cliParams)

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

// loadProjectConfig loads the project's .env and pgmi.yaml from sourcePath.
// .env is project-scoped (sourcePath/.env), never the process CWD, so the
// resolved target and credentials match the project being deployed.
// Returns nil config if pgmi.yaml does not exist (not an error).
func loadProjectConfig(sourcePath string, verbose bool) (*config.ProjectConfig, error) {
	envPath := filepath.Join(sourcePath, ".env")
	if err := godotenv.Load(envPath); err != nil && verbose {
		// A missing .env is normal and stays silent; surface only a real parse
		// error (file present but unreadable/malformed) so it isn't lost.
		if _, statErr := os.Stat(envPath); statErr == nil {
			fmt.Fprintf(os.Stderr, "[VERBOSE] failed to load %s: %v\n", envPath, err)
		}
	}

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
		SSLCert:            connConfig.SSLCert,
		SSLKey:             connConfig.SSLKey,
		SSLRootCert:        connConfig.SSLRootCert,
		AuthMethod:         authMethodToString(connConfig.AuthMethod),
		AzureTenantID:      connConfig.AzureTenantID,
		AzureClientID:      connConfig.AzureClientID,
		AWSRegion:          connConfig.AWSRegion,
		GoogleInstance:     connConfig.GoogleInstance,
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

		maps.Copy(parameters, fileParams)

		if verbose {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Loaded %d parameters from file (total: %d)\n", len(fileParams), len(parameters))
		}
	}

	return parameters, nil
}
