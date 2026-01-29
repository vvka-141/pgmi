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
	TenantID string // Overrides AZURE_TENANT_ID
	ClientID string // Overrides AZURE_CLIENT_ID
}

// IsEmpty returns true if no Azure flags were provided.
func (a *AzureFlags) IsEmpty() bool {
	return a == nil || (a.TenantID == "" && a.ClientID == "")
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
}

// LoadFromEnvironment loads PostgreSQL and cloud provider environment variables.
// This follows standard PostgreSQL client behavior and Azure SDK conventions.
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
// Azure Entra ID Authentication:
// If azureFlags are provided OR Azure environment variables are set (AZURE_TENANT_ID, etc.),
// the AuthMethod is set to AzureEntraID and credentials are attached to the config.
// CLI flags take precedence over environment variables.
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
	if envVars == nil {
		envVars = &EnvVars{}
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

	// If any Azure credentials are present, switch to Azure auth
	if tenantID != "" || clientID != "" {
		config.AuthMethod = pgmi.AuthMethodAzureEntraID
		config.AzureTenantID = tenantID
		config.AzureClientID = clientID
		config.AzureClientSecret = clientSecret
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
		cfg.Port = flags.Port
	} else if envVars.PGPORT != "" {
		port, err := strconv.Atoi(envVars.PGPORT)
		if err != nil {
			return nil, "", fmt.Errorf("invalid $PGPORT value '%s': must be an integer", envVars.PGPORT)
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

	// For granular parameters, maintenance database is always "postgres"
	// This is the standard database used for CREATE DATABASE operations
	maintenanceDB := pgmi.DefaultManagementDB // "postgres"

	return cfg, maintenanceDB, nil
}
