package db

import (
	"fmt"
	"os"
	"strconv"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// GranularConnFlags represents connection parameters from CLI flags.
// These follow PostgreSQL standard flag conventions (-h, -p, -U, -d).
//
// Note: Password is NOT included as a CLI flag for security reasons.
// Use one of these methods instead:
//   1. $PGPASSWORD environment variable
//   2. .pgpass file (PostgreSQL standard)
//   3. Connection string with embedded password
type GranularConnFlags struct {
	Host     string
	Port     int
	Username string
	Database string
	SSLMode  string
}

// AzureFlags represents Azure Entra ID CLI flags.
// These override the corresponding AZURE_* environment variables.
// Note: Client secret is NOT included as a CLI flag for security reasons.
// Use AZURE_CLIENT_SECRET environment variable instead.
type AzureFlags struct {
	Enabled  bool
	TenantID string // Overrides AZURE_TENANT_ID
	ClientID string // Overrides AZURE_CLIENT_ID
}

// IsEmpty returns true if no Azure flags were provided.
func (a *AzureFlags) IsEmpty() bool {
	return a == nil || (!a.Enabled && a.TenantID == "" && a.ClientID == "")
}

// AWSFlags represents AWS IAM authentication CLI flags.
// These override the corresponding AWS_* environment variables.
type AWSFlags struct {
	Enabled bool
	Region  string // Overrides AWS_REGION or AWS_DEFAULT_REGION
}

// IsEmpty returns true if no AWS flags were provided.
func (a *AWSFlags) IsEmpty() bool {
	return a == nil || (!a.Enabled && a.Region == "")
}

// GoogleFlags represents Google Cloud SQL IAM authentication CLI flags.
type GoogleFlags struct {
	Enabled  bool
	Instance string // Instance connection name: project:region:instance
}

// IsEmpty returns true if no Google flags were provided.
func (g *GoogleFlags) IsEmpty() bool {
	return g == nil || (!g.Enabled && g.Instance == "")
}

// CertFlags represents TLS client certificate CLI flags.
// These are additive — they can be combined with --connection or granular flags.
type CertFlags struct {
	SSLCert     string
	SSLKey      string
	SSLRootCert string
}

// IsEmpty returns true if no certificate flags were provided.
func (c *CertFlags) IsEmpty() bool {
	return c == nil || (c.SSLCert == "" && c.SSLKey == "" && c.SSLRootCert == "")
}

// IsEmpty returns true if no connection-related granular flags were provided by the user.
// Note: Database flag is excluded from this check because it can be used to override
// the database specified in a connection string.
func (g *GranularConnFlags) IsEmpty() bool {
	return g.Host == "" && g.Port == 0 && g.Username == "" && g.SSLMode == ""
}

// EnvVars represents PostgreSQL standard environment variables.
// See: https://www.postgresql.org/docs/current/libpq-envars.html
type EnvVars struct {
	PGHOST       string // PostgreSQL server host
	PGPORT       string // PostgreSQL server port
	PGUSER       string // PostgreSQL username
	PGPASSWORD   string // PostgreSQL password (discouraged, use .pgpass instead)
	PGDATABASE   string // Default database name
	PGSSLMODE    string // SSL mode
	DATABASE_URL string // Full connection string (Heroku/Rails convention)

	// Azure Entra ID environment variables (Azure SDK standard names)
	AZURE_TENANT_ID     string // Azure AD tenant/directory ID
	AZURE_CLIENT_ID     string // Azure AD application/client ID
	AZURE_CLIENT_SECRET string // Azure AD client secret (for Service Principal auth)

	// AWS IAM environment variables (AWS SDK standard names)
	AWS_REGION         string // AWS region for RDS IAM auth
	AWS_DEFAULT_REGION string // Fallback region (AWS SDK convention)

	// TLS client certificate environment variables (PostgreSQL standard)
	PGSSLCERT     string // Client certificate path
	PGSSLKEY      string // Client key path
	PGSSLROOTCERT string // Root CA certificate path
	PGSSLPASSWORD string // Client key password
}

// LoadFromEnvironment loads PostgreSQL and cloud provider environment variables.
// This follows standard PostgreSQL client behavior and Azure/AWS SDK conventions.
func LoadFromEnvironment() *EnvVars {
	return &EnvVars{
		PGHOST:              os.Getenv("PGHOST"),
		PGPORT:              os.Getenv("PGPORT"),
		PGUSER:              os.Getenv("PGUSER"),
		PGPASSWORD:          os.Getenv("PGPASSWORD"),
		PGDATABASE:          os.Getenv("PGDATABASE"),
		PGSSLMODE:           os.Getenv("PGSSLMODE"),
		DATABASE_URL:        os.Getenv("DATABASE_URL"),
		AZURE_TENANT_ID:     os.Getenv("AZURE_TENANT_ID"),
		AZURE_CLIENT_ID:     os.Getenv("AZURE_CLIENT_ID"),
		AZURE_CLIENT_SECRET: os.Getenv("AZURE_CLIENT_SECRET"),
		AWS_REGION:          os.Getenv("AWS_REGION"),
		AWS_DEFAULT_REGION:  os.Getenv("AWS_DEFAULT_REGION"),
		PGSSLCERT:           os.Getenv("PGSSLCERT"),
		PGSSLKEY:            os.Getenv("PGSSLKEY"),
		PGSSLROOTCERT:       os.Getenv("PGSSLROOTCERT"),
		PGSSLPASSWORD:       os.Getenv("PGSSLPASSWORD"),
	}
}

// HasAzureCredentials returns true if Azure Entra ID environment variables are set.
func (e *EnvVars) HasAzureCredentials() bool {
	return e.AZURE_TENANT_ID != "" || e.AZURE_CLIENT_ID != ""
}

// ResolveConnectionParams resolves connection parameters using PostgreSQL-standard precedence:
//
// 1. Connection string flag (--connection) - if provided, parse and use directly
// 2. Granular flags (-h, -p, -U, -d) - if any provided, build config from flags
// 3. Environment variables (PGHOST, PGPORT, etc.) - fallback if no flags
// 4. DATABASE_URL environment variable - fallback if no granular params
// 5. Defaults (localhost:5432, prefer SSL)
//
// Cloud Authentication:
// If azureFlags/awsFlags/googleFlags are provided OR corresponding environment variables are set,
// the AuthMethod is set accordingly and credentials are attached to the config.
// CLI flags take precedence over environment variables.
// Azure, AWS, and Google flags are mutually exclusive.
//
// Returns:
//   - ConnectionConfig with all parameters resolved
//   - Maintenance database name (for CREATE DATABASE operations)
//   - Error if configuration is invalid or conflicting
//
// Conflict Detection:
// Returns error if BOTH --connection flag AND granular flags are provided.
// This prevents ambiguity and ensures clear user intent.
func ResolveConnectionParams(
	connStringFlag string,
	granularFlags *GranularConnFlags,
	azureFlags *AzureFlags,
	awsFlags *AWSFlags,
	googleFlags *GoogleFlags,
	certFlags *CertFlags,
	envVars *EnvVars,
	projectConfig *config.ProjectConfig,
) (*pgmi.ConnectionConfig, string, error) {
	// Validate inputs
	if granularFlags == nil {
		granularFlags = &GranularConnFlags{}
	}
	if azureFlags == nil {
		azureFlags = &AzureFlags{}
	}
	if awsFlags == nil {
		awsFlags = &AWSFlags{}
	}
	if googleFlags == nil {
		googleFlags = &GoogleFlags{}
	}
	if envVars == nil {
		envVars = &EnvVars{}
	}

	// Check for conflicts: cannot use multiple cloud auth methods
	enabledCount := 0
	if !azureFlags.IsEmpty() {
		enabledCount++
	}
	if !awsFlags.IsEmpty() {
		enabledCount++
	}
	if !googleFlags.IsEmpty() {
		enabledCount++
	}
	if enabledCount > 1 {
		return nil, "", fmt.Errorf("cannot use multiple cloud authentication methods; choose one of --azure, --aws, or --google")
	}

	// Check for conflicts: connection string XOR granular flags
	if connStringFlag != "" && !granularFlags.IsEmpty() {
		return nil, "", fmt.Errorf(
			"cannot specify both --connection and granular flags (-h, -p, -U)\n" +
				"Choose one approach:\n" +
				"  1. Connection string: --connection \"postgresql://user@localhost:5432/postgres\"\n" +
				"  2. Granular flags: -h localhost -p 5432 -U myuser -d mydb\n" +
				"  3. Environment variables: export PGHOST=localhost PGPORT=5432 PGUSER=myuser",
		)
	}

	var config *pgmi.ConnectionConfig
	var maintenanceDB string
	var err error

	// Path 1: Connection string provided via --connection flag
	if connStringFlag != "" {
		config, maintenanceDB, err = resolveFromConnectionString(connStringFlag, envVars)
	} else if granularFlags.IsEmpty() && envVars.DATABASE_URL != "" {
		// Path 2: DATABASE_URL environment variable (if no granular flags)
		config, maintenanceDB, err = resolveFromConnectionString(envVars.DATABASE_URL, envVars)
	} else {
		// Path 3: Granular flags + environment variables with precedence
		config, maintenanceDB, err = resolveFromGranularParams(granularFlags, envVars, projectConfig)
	}

	if err != nil {
		return nil, "", err
	}

	// Apply Azure Entra ID authentication if configured
	applyAzureAuth(config, azureFlags, envVars)

	// Apply AWS IAM authentication if configured
	applyAWSAuth(config, awsFlags, envVars)

	// Apply Google Cloud SQL IAM authentication if configured
	applyGoogleAuth(config, googleFlags)

	// Apply TLS client certificate parameters
	applyCertParams(config, certFlags, envVars, projectConfig)

	return config, maintenanceDB, nil
}

// applyAzureAuth sets Azure Entra ID authentication on the config if credentials are available.
// CLI flags take precedence over environment variables.
func applyAzureAuth(config *pgmi.ConnectionConfig, flags *AzureFlags, env *EnvVars) {
	// Determine tenant ID: flag > env var
	tenantID := flags.TenantID
	if tenantID == "" {
		tenantID = env.AZURE_TENANT_ID
	}

	// Determine client ID: flag > env var
	clientID := flags.ClientID
	if clientID == "" {
		clientID = env.AZURE_CLIENT_ID
	}

	// Client secret only comes from env var (no flag for security)
	clientSecret := env.AZURE_CLIENT_SECRET

	if flags.Enabled {
		config.AuthMethod = pgmi.AuthMethodAzureEntraID
		config.AzureTenantID = tenantID
		config.AzureClientID = clientID
		config.AzureClientSecret = clientSecret
	}
}

// applyAWSAuth sets AWS IAM authentication on the config if enabled.
// CLI flags take precedence over environment variables.
func applyAWSAuth(config *pgmi.ConnectionConfig, flags *AWSFlags, env *EnvVars) {
	// Determine region: flag > AWS_REGION > AWS_DEFAULT_REGION
	region := flags.Region
	if region == "" {
		region = env.AWS_REGION
	}
	if region == "" {
		region = env.AWS_DEFAULT_REGION
	}

	// If AWS auth is enabled (via flag), switch to AWS IAM auth
	if flags.Enabled {
		config.AuthMethod = pgmi.AuthMethodAWSIAM
		config.AWSRegion = region
	}
}

// applyGoogleAuth sets Google Cloud SQL IAM authentication on the config if enabled.
func applyGoogleAuth(config *pgmi.ConnectionConfig, flags *GoogleFlags) {
	if flags.Enabled {
		config.AuthMethod = pgmi.AuthMethodGoogleIAM
		config.GoogleInstance = flags.Instance
	}
}

// applyCertParams sets TLS client certificate parameters on the config.
// Precedence: flag > env var > pgmi.yaml > existing (from connection string).
// SSLPassword is only available from env var (no flag, no yaml — security).
func applyCertParams(cfg *pgmi.ConnectionConfig, flags *CertFlags, env *EnvVars, pc *config.ProjectConfig) {
	var yamlCert, yamlKey, yamlRootCert string
	if pc != nil {
		yamlCert = pc.Connection.SSLCert
		yamlKey = pc.Connection.SSLKey
		yamlRootCert = pc.Connection.SSLRootCert
	}

	if flags != nil && flags.SSLCert != "" {
		cfg.SSLCert = flags.SSLCert
	} else if env != nil && env.PGSSLCERT != "" {
		cfg.SSLCert = env.PGSSLCERT
	} else if yamlCert != "" {
		cfg.SSLCert = yamlCert
	}

	if flags != nil && flags.SSLKey != "" {
		cfg.SSLKey = flags.SSLKey
	} else if env != nil && env.PGSSLKEY != "" {
		cfg.SSLKey = env.PGSSLKEY
	} else if yamlKey != "" {
		cfg.SSLKey = yamlKey
	}

	if flags != nil && flags.SSLRootCert != "" {
		cfg.SSLRootCert = flags.SSLRootCert
	} else if env != nil && env.PGSSLROOTCERT != "" {
		cfg.SSLRootCert = env.PGSSLROOTCERT
	} else if yamlRootCert != "" {
		cfg.SSLRootCert = yamlRootCert
	}

	if env != nil && env.PGSSLPASSWORD != "" {
		cfg.SSLPassword = env.PGSSLPASSWORD
	}
}

// resolveFromConnectionString parses a connection string and derives the maintenance database.
//
// The database component of the connection string serves dual purpose:
// 1. Initial connection target (for CREATE DATABASE operations)
// 2. Maintenance database (returned separately)
//
// The actual target database comes from --database/-d flag.
//
// Environment variables are applied as fallbacks for parameters not specified
// in the connection string (following PostgreSQL standard behavior).
func resolveFromConnectionString(connStr string, envVars *EnvVars) (*pgmi.ConnectionConfig, string, error) {
	config, err := ParseConnectionString(connStr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid connection string: %w", err)
	}

	// Apply PGSSLMODE from environment if not specified in connection string
	// This follows PostgreSQL's libpq behavior where environment variables
	// serve as fallbacks for connection string parameters
	if config.SSLMode == "" && envVars != nil && envVars.PGSSLMODE != "" {
		config.SSLMode = envVars.PGSSLMODE
	}
	// Default to "prefer" if still not set
	if config.SSLMode == "" {
		config.SSLMode = "prefer"
	}

	// The database in the connection string becomes the maintenance DB
	// This is used for server-level operations (CREATE DATABASE, DROP DATABASE)
	maintenanceDB := config.Database
	if maintenanceDB == "" {
		maintenanceDB = pgmi.DefaultManagementDB // "postgres"
	}

	return config, maintenanceDB, nil
}

// resolveFromGranularParams builds ConnectionConfig from granular flags and environment variables.
//
// Precedence for each parameter (following PostgreSQL standards):
// 1. CLI flag (highest priority)
// 2. Environment variable
// 3. Default value (lowest priority)
//
// For granular parameters, the maintenance database is always "postgres".
func resolveFromGranularParams(
	flags *GranularConnFlags,
	envVars *EnvVars,
	projectConfig *config.ProjectConfig,
) (*pgmi.ConnectionConfig, string, error) {
	cfg := &pgmi.ConnectionConfig{
		AuthMethod:       pgmi.AuthMethodStandard,
		AdditionalParams: make(map[string]string),
	}

	var pc config.ConnectionConfig
	if projectConfig != nil {
		pc = projectConfig.Connection
	}

	// Host: flag > PGHOST > pgmi.yaml > default
	cfg.Host = flags.Host
	if cfg.Host == "" {
		cfg.Host = envVars.PGHOST
	}
	if cfg.Host == "" {
		cfg.Host = pc.Host
	}
	if cfg.Host == "" {
		cfg.Host = "localhost"
	}

	// Port: flag > PGPORT > pgmi.yaml > default
	if flags.Port != 0 {
		if err := validatePort(flags.Port); err != nil {
			return nil, "", fmt.Errorf("invalid --port flag: %w", err)
		}
		cfg.Port = flags.Port
	} else if envVars.PGPORT != "" {
		port, err := strconv.Atoi(envVars.PGPORT)
		if err != nil {
			return nil, "", fmt.Errorf("invalid $PGPORT value '%s': must be an integer", envVars.PGPORT)
		}
		if err := validatePort(port); err != nil {
			return nil, "", fmt.Errorf("invalid $PGPORT value '%s': %w", envVars.PGPORT, err)
		}
		cfg.Port = port
	} else if pc.Port != 0 {
		cfg.Port = pc.Port
	} else {
		cfg.Port = 5432
	}

	// Username: flag > PGUSER > pgmi.yaml > current OS user
	cfg.Username = flags.Username
	if cfg.Username == "" {
		cfg.Username = envVars.PGUSER
	}
	if cfg.Username == "" {
		cfg.Username = pc.Username
	}
	if cfg.Username == "" {
		if currentUser := os.Getenv("USER"); currentUser != "" {
			cfg.Username = currentUser
		} else if currentUser := os.Getenv("USERNAME"); currentUser != "" {
			cfg.Username = currentUser
		}
	}

	cfg.Password = envVars.PGPASSWORD

	// Database: flag > PGDATABASE > pgmi.yaml
	cfg.Database = flags.Database
	if cfg.Database == "" {
		cfg.Database = envVars.PGDATABASE
	}
	if cfg.Database == "" {
		cfg.Database = pc.Database
	}

	// SSLMode: flag > PGSSLMODE > pgmi.yaml > default
	cfg.SSLMode = flags.SSLMode
	if cfg.SSLMode == "" {
		cfg.SSLMode = envVars.PGSSLMODE
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = pc.SSLMode
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = "prefer"
	}

	// Management database: pgmi.yaml > default ("postgres")
	maintenanceDB := pc.ManagementDatabase
	if maintenanceDB == "" {
		maintenanceDB = pgmi.DefaultManagementDB
	}

	return cfg, maintenanceDB, nil
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %d: must be between 1 and 65535", port)
	}
	return nil
}
