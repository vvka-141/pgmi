package db

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// GranularConnFlags represents connection parameters from CLI flags.
// These follow PostgreSQL standard flag conventions (-h, -p, -U, -d).
//
// Note: Password is NOT included as a CLI flag for security reasons.
// Use one of these methods instead:
//  1. $PGPASSWORD environment variable
//  2. .pgpass file (PostgreSQL standard)
//  3. Connection string with embedded password
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

// serverFlags names the flags that pick a server or a login: -h, -p, -U.
func (g *GranularConnFlags) serverFlags() []string {
	var names []string
	if g.Host != "" {
		names = append(names, "-h")
	}
	if g.Port != 0 {
		names = append(names, "-p")
	}
	if g.Username != "" {
		names = append(names, "-U")
	}
	return names
}

// EnvVars represents PostgreSQL standard environment variables.
// See: https://www.postgresql.org/docs/current/libpq-envars.html
type EnvVars struct {
	PGHOST                 string // PostgreSQL server host
	PGPORT                 string // PostgreSQL server port
	PGUSER                 string // PostgreSQL username
	PGPASSWORD             string // PostgreSQL password (discouraged, use .pgpass instead)
	PGDATABASE             string // Default database name
	PGSSLMODE              string // SSL mode
	PGAPPNAME              string // application_name reported in pg_stat_activity
	PGCONNECT_TIMEOUT      string // Connection timeout in seconds (libpq convention)
	PGSERVICE              string // libpq service file entry; refused, pgmi reads no service files
	DATABASE_URL           string // Full connection string (Heroku/Rails convention)
	PGMI_CONNECTION_STRING string

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
		PGHOST:                 os.Getenv("PGHOST"),
		PGPORT:                 os.Getenv("PGPORT"),
		PGUSER:                 os.Getenv("PGUSER"),
		PGPASSWORD:             os.Getenv("PGPASSWORD"),
		PGDATABASE:             os.Getenv("PGDATABASE"),
		PGSSLMODE:              os.Getenv("PGSSLMODE"),
		PGAPPNAME:              os.Getenv("PGAPPNAME"),
		PGCONNECT_TIMEOUT:      os.Getenv("PGCONNECT_TIMEOUT"),
		PGSERVICE:              os.Getenv("PGSERVICE"),
		DATABASE_URL:           os.Getenv("DATABASE_URL"),
		PGMI_CONNECTION_STRING: os.Getenv("PGMI_CONNECTION_STRING"),
		AZURE_TENANT_ID:        os.Getenv("AZURE_TENANT_ID"),
		AZURE_CLIENT_ID:        os.Getenv("AZURE_CLIENT_ID"),
		AZURE_CLIENT_SECRET:    os.Getenv("AZURE_CLIENT_SECRET"),
		AWS_REGION:             os.Getenv("AWS_REGION"),
		AWS_DEFAULT_REGION:     os.Getenv("AWS_DEFAULT_REGION"),
		PGSSLCERT:              os.Getenv("PGSSLCERT"),
		PGSSLKEY:               os.Getenv("PGSSLKEY"),
		PGSSLROOTCERT:          os.Getenv("PGSSLROOTCERT"),
		PGSSLPASSWORD:          os.Getenv("PGSSLPASSWORD"),
	}
}

// ResolveConnectionParams resolves connection parameters using PostgreSQL-standard precedence:
//
// 1. Connection string flag (--connection) - if provided, parse and use directly
// 2. Granular flags (-h, -p, -U, --sslmode) - if any provided, build config from flags
// 3. PGMI_CONNECTION_STRING, then DATABASE_URL - only when no granular flag is given
// 4. Environment variables (PGHOST, PGPORT, etc.), then pgmi.yaml
// 5. Defaults (localhost:5432; sslmode prefer, or a TLS-only mode under token auth)
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
) (*ConnectionConfig, string, error) {
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
	if envVars.PGSERVICE != "" {
		return nil, "", fmt.Errorf("PGSERVICE is set: %w: %w", errServiceFile, pgmi.ErrInvalidConfig)
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
		return nil, "", fmt.Errorf("cannot use multiple cloud authentication methods; choose one of --azure, --aws, or --google: %w", pgmi.ErrInvalidConfig)
	}

	if connStringFlag != "" && !granularFlags.IsEmpty() {
		passed := granularFlags.serverFlags()
		hint := "put the host, port and user in the connection string, or drop --connection and use -h, -p, -U"
		if granularFlags.SSLMode != "" {
			passed = append(passed, "--sslmode")
			if len(passed) == 1 {
				hint = fmt.Sprintf("put sslmode=%s in the connection string", granularFlags.SSLMode)
			}
		}
		return nil, "", fmt.Errorf("cannot combine --connection with %s; %s: %w",
			strings.Join(passed, ", "), hint, pgmi.ErrInvalidConfig)
	}
	if connStringFlag == "" {
		if _, source := SelectConnectionString("", nil, envVars); source != "" {
			if passed := granularFlags.serverFlags(); len(passed) > 0 {
				return nil, "", fmt.Errorf("cannot combine %s with %s, which names its own server and login; "+
					"unset %s to connect with the flags, or put the values in the string: %w",
					strings.Join(passed, ", "), source, source, pgmi.ErrInvalidConfig)
			}
		}
	}

	var config *ConnectionConfig
	var maintenanceDB, sslModeSource string
	var err error

	if connStr, source := SelectConnectionString(connStringFlag, granularFlags, envVars); connStr != "" {
		config, maintenanceDB, sslModeSource, err = resolveFromConnectionString(connStr, source, envVars)
		if err != nil && source != "--connection" {
			err = fmt.Errorf("%s: %w", source, err)
		}
		if err == nil && granularFlags.SSLMode != "" {
			config.SSLMode, sslModeSource = granularFlags.SSLMode, "--sslmode"
		}
	} else {
		config, maintenanceDB, sslModeSource, err = resolveFromGranularParams(granularFlags, envVars, projectConfig)
	}

	if err != nil {
		return nil, "", err
	}

	// Apply cloud authentication (flag > env var > pgmi.yaml). --azure, --aws
	// or --google outranks pgmi.yaml's auth_method; a detail flag such as
	// --aws-region refines the method pgmi.yaml chose.
	authConfig := projectConfig
	if projectConfig != nil && (azureFlags.Enabled || awsFlags.Enabled || googleFlags.Enabled) {
		withoutMethod := *projectConfig
		withoutMethod.Connection.AuthMethod = ""
		authConfig = &withoutMethod
	}
	applyAzureAuth(config, azureFlags, envVars, authConfig)
	applyAWSAuth(config, awsFlags, envVars, authConfig)
	applyGoogleAuth(config, googleFlags, authConfig)

	// Apply TLS client certificate parameters
	applyCertParams(config, certFlags, envVars, projectConfig)

	if err := ApplySSLMode(config, sslModeSource); err != nil {
		return nil, "", err
	}

	return config, maintenanceDB, nil
}

var tokenAuthSSLModes = []string{"require", "verify-ca", "verify-full"}

// ApplySSLMode fills in the default sslmode and refuses modes that can fall
// back to plaintext when the password is an IAM token (Azure, AWS). It runs
// after auth and certificate resolution because an unset mode and an explicit
// "prefer" must stay distinguishable until the auth method is known.
//
// Azure's server certificates chain to public roots (DigiCert Global Root G2,
// Microsoft RSA Root CA 2017) that the system trust store holds, so verify-full
// works without configuration. RDS certificates chain to private Amazon RDS
// CAs, so verify-full needs the RDS bundle in sslrootcert; without one the
// default is require.
func ApplySSLMode(cfg *ConnectionConfig, source string) error {
	tokenAuth := cfg.AuthMethod == pgmi.AuthMethodAzureEntraID || cfg.AuthMethod == pgmi.AuthMethodAWSIAM

	if cfg.SSLMode == "" {
		switch {
		case cfg.AuthMethod == pgmi.AuthMethodAzureEntraID, tokenAuth && cfg.SSLRootCert != "":
			cfg.SSLMode = "verify-full"
		case tokenAuth:
			cfg.SSLMode = "require"
		default:
			cfg.SSLMode = "prefer"
		}
		return nil
	}

	if tokenAuth && !slices.Contains(tokenAuthSSLModes, cfg.SSLMode) {
		return fmt.Errorf(
			"sslmode %q from %s is not allowed with %s authentication: it can send the token without TLS; use require, verify-ca or verify-full: %w",
			cfg.SSLMode, source, cfg.AuthMethod, pgmi.ErrInvalidConfig,
		)
	}
	return nil
}

// SelectConnectionString returns the connection string the resolver uses and
// where it came from: the --connection flag, else PGMI_CONNECTION_STRING, else
// DATABASE_URL. With -h, -p or -U an environment string is not used (the
// resolver refuses that combination); --sslmode applies on top of it.
func SelectConnectionString(connStringFlag string, granularFlags *GranularConnFlags, envVars *EnvVars) (connStr, source string) {
	if connStringFlag != "" {
		return connStringFlag, "--connection"
	}
	if envVars == nil || (granularFlags != nil && len(granularFlags.serverFlags()) > 0) {
		return "", ""
	}
	if envVars.PGMI_CONNECTION_STRING != "" {
		return envVars.PGMI_CONNECTION_STRING, "PGMI_CONNECTION_STRING"
	}
	if envVars.DATABASE_URL != "" {
		return envVars.DATABASE_URL, "DATABASE_URL"
	}
	return "", ""
}

// applyAzureAuth sets Azure Entra ID authentication on the config if credentials are available.
// CLI flags take precedence over environment variables.
func applyAzureAuth(cfg *ConnectionConfig, flags *AzureFlags, env *EnvVars, pc *config.ProjectConfig) {
	var yamlTenantID, yamlClientID, yamlAuthMethod string
	if pc != nil {
		yamlTenantID = pc.Connection.AzureTenantID
		yamlClientID = pc.Connection.AzureClientID
		yamlAuthMethod = pc.Connection.AuthMethod
	}

	enabled := flags.Enabled || yamlAuthMethod == "azure"

	// Determine tenant ID: flag > env var > pgmi.yaml
	tenantID := flags.TenantID
	if tenantID == "" {
		tenantID = env.AZURE_TENANT_ID
	}
	if tenantID == "" {
		tenantID = yamlTenantID
	}

	// Determine client ID: flag > env var > pgmi.yaml
	clientID := flags.ClientID
	if clientID == "" {
		clientID = env.AZURE_CLIENT_ID
	}
	if clientID == "" {
		clientID = yamlClientID
	}

	// Client secret only comes from env var (no flag/yaml for security)
	clientSecret := env.AZURE_CLIENT_SECRET

	if enabled {
		cfg.AuthMethod = pgmi.AuthMethodAzureEntraID
		cfg.AzureTenantID = tenantID
		cfg.AzureClientID = clientID
		cfg.AzureClientSecret = clientSecret
	}
}

// applyAWSAuth sets AWS IAM authentication on the config if enabled.
// CLI flags take precedence over environment variables.
func applyAWSAuth(cfg *ConnectionConfig, flags *AWSFlags, env *EnvVars, pc *config.ProjectConfig) {
	var yamlRegion, yamlAuthMethod string
	if pc != nil {
		yamlRegion = pc.Connection.AWSRegion
		yamlAuthMethod = pc.Connection.AuthMethod
	}

	enabled := flags.Enabled || yamlAuthMethod == "aws"

	// Determine region: flag > AWS_REGION > AWS_DEFAULT_REGION > pgmi.yaml
	region := flags.Region
	if region == "" {
		region = env.AWS_REGION
	}
	if region == "" {
		region = env.AWS_DEFAULT_REGION
	}
	if region == "" {
		region = yamlRegion
	}

	if enabled {
		cfg.AuthMethod = pgmi.AuthMethodAWSIAM
		cfg.AWSRegion = region
	}
}

func applyGoogleAuth(cfg *ConnectionConfig, flags *GoogleFlags, pc *config.ProjectConfig) {
	var yamlInstance, yamlAuthMethod string
	if pc != nil {
		yamlInstance = pc.Connection.GoogleInstance
		yamlAuthMethod = pc.Connection.AuthMethod
	}

	enabled := flags.Enabled || yamlAuthMethod == "google"

	instance := flags.Instance
	if instance == "" {
		instance = yamlInstance
	}

	if enabled {
		cfg.AuthMethod = pgmi.AuthMethodGoogleIAM
		cfg.GoogleInstance = instance
	}
}

// applyCertParams sets TLS client certificate parameters on the config.
// Precedence: CLI flag > connection string > env var > pgmi.yaml.
//
// The connection string outranks the environment because libpq says so: its
// environment variables select "default connection parameter values ... if no
// value is directly specified by the calling code", and PGSSLCERT, PGSSLKEY,
// PGSSLROOTCERT and PGSSLPASSWORD each behave the same as their connection
// parameter. Reversed, a stale PGSSLROOTCERT swaps the trust anchor out from
// under a verify-full connection string, and PGSSLCERT swaps the mTLS client
// identity — both silently.
//
// cfg carries the connection-string values on entry. The granular-flag path
// leaves these four fields empty, so a non-empty value here came from the string.
func applyCertParams(cfg *ConnectionConfig, flags *CertFlags, env *EnvVars, pc *config.ProjectConfig) {
	var yamlCert, yamlKey, yamlRootCert string
	if pc != nil {
		yamlCert = pc.Connection.SSLCert
		yamlKey = pc.Connection.SSLKey
		yamlRootCert = pc.Connection.SSLRootCert
	}

	var flagCert, flagKey, flagRootCert string
	if flags != nil {
		flagCert, flagKey, flagRootCert = flags.SSLCert, flags.SSLKey, flags.SSLRootCert
	}

	var envCert, envKey, envRootCert, envPassword string
	if env != nil {
		envCert, envKey = env.PGSSLCERT, env.PGSSLKEY
		envRootCert, envPassword = env.PGSSLROOTCERT, env.PGSSLPASSWORD
	}

	cfg.SSLCert = firstNonEmpty(flagCert, cfg.SSLCert, envCert, yamlCert)
	cfg.SSLKey = firstNonEmpty(flagKey, cfg.SSLKey, envKey, yamlKey)
	cfg.SSLRootCert = firstNonEmpty(flagRootCert, cfg.SSLRootCert, envRootCert, yamlRootCert)

	// The private-key passphrase has no flag and no pgmi.yaml field by design:
	// it belongs in neither argv nor a committed file.
	cfg.SSLPassword = firstNonEmpty(cfg.SSLPassword, envPassword)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Override is one pgmi.yaml connection value that another source replaced. An
// empty Value means the yaml value was dropped: a connection string never
// takes sslmode from pgmi.yaml.
type Override struct {
	Field    string `json:"field"`
	From     string `json:"from"`
	Value    string `json:"value"`
	NotFrom  string `json:"notFrom"`
	NotValue string `json:"notValue"`
}

func (o Override) String() string {
	if o.Value == "" {
		return fmt.Sprintf("%s: pgmi.yaml (%s) does not apply to the connection string from %s; set %s in the string", o.Field, o.NotValue, o.From, o.Field)
	}
	return fmt.Sprintf("%s: from %s (%s), not %s (%s)", o.Field, o.From, o.Value, o.NotFrom, o.NotValue)
}

// EnvOverrides names each pgmi.yaml connection value that a PG* variable or the
// connection string replaces, so the replacement is never silent. With a
// connection string pgmi.yaml supplies only the database and the certificate
// paths; its sslmode never applies.
func EnvOverrides(connStr, source string, flags *GranularConnFlags, certs *CertFlags, env *EnvVars, pc *config.ProjectConfig) []Override {
	if env == nil {
		return nil
	}
	if pc == nil {
		pc = &config.ProjectConfig{}
	}
	if flags == nil {
		flags = &GranularConnFlags{}
	}
	if certs == nil {
		certs = &CertFlags{}
	}
	y := pc.Connection
	type field struct{ name, from, value, yamlValue, flagValue string }
	var raw *ConnectionConfig
	var fields []field
	if connStr != "" {
		var err error
		if raw, err = parseConnectionStringRaw(connStr); err != nil {
			return nil
		}
		if raw.Database == "" {
			fields = append(fields, field{"database", "PGDATABASE", env.PGDATABASE, y.Database, flags.Database})
		} else {
			fields = append(fields, field{"database", source, raw.Database, y.Database, flags.Database})
		}
	} else {
		port := ""
		if y.Port != 0 {
			port = strconv.Itoa(y.Port)
		}
		portFlag := ""
		if flags.Port != 0 {
			portFlag = strconv.Itoa(flags.Port)
		}
		fields = append(fields,
			field{"database", "PGDATABASE", env.PGDATABASE, y.Database, flags.Database},
			field{"host", "PGHOST", env.PGHOST, y.Host, flags.Host},
			field{"port", "PGPORT", env.PGPORT, port, portFlag},
			field{"user", "PGUSER", env.PGUSER, y.Username, flags.Username},
			field{"sslmode", "PGSSLMODE", env.PGSSLMODE, y.SSLMode, flags.SSLMode})
	}

	var notes []Override
	if raw != nil && flags.SSLMode != "" && raw.SSLMode != "" && raw.SSLMode != flags.SSLMode {
		notes = append(notes, Override{Field: "sslmode", From: "--sslmode", Value: flags.SSLMode, NotFrom: source, NotValue: raw.SSLMode})
	}
	if raw != nil && y.SSLMode != "" && flags.SSLMode == "" {
		switch {
		case raw.SSLMode != "":
			fields = append(fields, field{"sslmode", source, raw.SSLMode, y.SSLMode, ""})
		case env.PGSSLMODE != "":
			fields = append(fields, field{"sslmode", "PGSSLMODE", env.PGSSLMODE, y.SSLMode, ""})
		case y.SSLMode != "prefer":
			notes = append(notes, Override{Field: "sslmode", From: source, NotFrom: "pgmi.yaml", NotValue: y.SSLMode})
		}
	}

	str := raw
	if str == nil {
		str = &ConnectionConfig{}
	}
	for _, c := range []struct{ name, envName, envValue, yamlValue, flagValue, strValue string }{
		{"sslcert", "PGSSLCERT", env.PGSSLCERT, y.SSLCert, certs.SSLCert, str.SSLCert},
		{"sslkey", "PGSSLKEY", env.PGSSLKEY, y.SSLKey, certs.SSLKey, str.SSLKey},
		{"sslrootcert", "PGSSLROOTCERT", env.PGSSLROOTCERT, y.SSLRootCert, certs.SSLRootCert, str.SSLRootCert},
	} {
		if c.strValue != "" {
			fields = append(fields, field{c.name, source, c.strValue, c.yamlValue, c.flagValue})
		} else {
			fields = append(fields, field{c.name, c.envName, c.envValue, c.yamlValue, c.flagValue})
		}
	}

	for _, f := range fields {
		if f.flagValue == "" && f.value != "" && f.yamlValue != "" && f.value != f.yamlValue {
			notes = append(notes, Override{Field: f.name, From: f.from, Value: f.value, NotFrom: "pgmi.yaml", NotValue: f.yamlValue})
		}
	}
	return notes
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
func resolveFromConnectionString(connStr, source string, envVars *EnvVars) (*ConnectionConfig, string, string, error) {
	config, err := parseConnectionStringRaw(connStr)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid connection string: %w: %w", err, pgmi.ErrInvalidConfig)
	}

	// libpq takes every parameter the string leaves out from PG*, then from its
	// defaults; explicit keywords win.
	if envVars != nil {
		if config.Host == "" {
			config.Host = envVars.PGHOST
		}
		if config.Port == 0 && envVars.PGPORT != "" {
			port, err := strconv.Atoi(envVars.PGPORT)
			if err != nil || validatePort(port) != nil {
				return nil, "", "", fmt.Errorf("invalid $PGPORT value '%s': %w", envVars.PGPORT, pgmi.ErrInvalidConfig)
			}
			config.Port = port
		}
		if config.Database == "" {
			config.Database = envVars.PGDATABASE
		}
		if config.Username == "" {
			config.Username = envVars.PGUSER
		}
		if config.AppName == "" {
			config.AppName = envVars.PGAPPNAME
		}
		if config.ConnectTimeout == 0 && envVars.PGCONNECT_TIMEOUT != "" {
			seconds, err := strconv.Atoi(envVars.PGCONNECT_TIMEOUT)
			if err != nil || seconds < 0 {
				return nil, "", "", fmt.Errorf("invalid $PGCONNECT_TIMEOUT value '%s': must be a non-negative integer (seconds): %w", envVars.PGCONNECT_TIMEOUT, pgmi.ErrInvalidConfig)
			}
			config.ConnectTimeout = time.Duration(seconds) * time.Second
		}
	}
	database := config.Database
	applyStaticDefaults(config)
	config.Database = database
	if config.AppName == "" {
		config.AppName = "pgmi"
	}

	// PGSSLMODE is a fallback for a connection string without sslmode (libpq behavior).
	sslModeSource := source
	if config.SSLMode == "" && envVars != nil && envVars.PGSSLMODE != "" {
		config.SSLMode = envVars.PGSSLMODE
		sslModeSource = "$PGSSLMODE"
	}

	// The database in the connection string becomes the maintenance DB
	// This is used for server-level operations (CREATE DATABASE, DROP DATABASE).
	// A string without one leaves the target unset: libpq would default it to
	// the user name, and pgmi used to default it to postgres, deploying there.
	maintenanceDB := config.Database
	if maintenanceDB == "" {
		maintenanceDB = DefaultMaintenanceDB // "postgres"
	}

	return config, maintenanceDB, sslModeSource, nil
}

// resolveFromGranularParams builds ConnectionConfig from granular flags and environment variables.
//
// Precedence for each parameter (following PostgreSQL standards):
// 1. CLI flag (highest priority)
// 2. Environment variable
// 3. Default value (lowest priority)
//
// For granular parameters, the maintenance database is pgmi.yaml's
// connection.maintenance_database, else "postgres".
func resolveFromGranularParams(
	flags *GranularConnFlags,
	envVars *EnvVars,
	projectConfig *config.ProjectConfig,
) (*ConnectionConfig, string, string, error) {
	cfg := &ConnectionConfig{
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
			return nil, "", "", fmt.Errorf("invalid --port flag: %w: %w", err, pgmi.ErrInvalidConfig)
		}
		cfg.Port = flags.Port
	} else if envVars.PGPORT != "" {
		port, err := strconv.Atoi(envVars.PGPORT)
		if err != nil {
			return nil, "", "", fmt.Errorf("invalid $PGPORT value '%s': must be an integer: %w", envVars.PGPORT, pgmi.ErrInvalidConfig)
		}
		if err := validatePort(port); err != nil {
			return nil, "", "", fmt.Errorf("invalid $PGPORT value '%s': %w: %w", envVars.PGPORT, err, pgmi.ErrInvalidConfig)
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

	// SSLMode: flag > PGSSLMODE > pgmi.yaml; ApplySSLMode supplies the default
	var sslModeSource string
	switch {
	case flags.SSLMode != "":
		cfg.SSLMode, sslModeSource = flags.SSLMode, "--sslmode"
	case envVars.PGSSLMODE != "":
		cfg.SSLMode, sslModeSource = envVars.PGSSLMODE, "$PGSSLMODE"
	case pc.SSLMode != "":
		cfg.SSLMode, sslModeSource = pc.SSLMode, "pgmi.yaml connection.sslmode"
	}

	// AppName: PGAPPNAME > "pgmi" default. Surfaces in pg_stat_activity.application_name
	// so DBAs can attribute live sessions to pgmi deployments.
	cfg.AppName = envVars.PGAPPNAME
	if cfg.AppName == "" {
		cfg.AppName = "pgmi"
	}

	// ConnectTimeout: PGCONNECT_TIMEOUT (seconds) — libpq convention.
	if envVars.PGCONNECT_TIMEOUT != "" {
		seconds, err := strconv.Atoi(envVars.PGCONNECT_TIMEOUT)
		if err != nil || seconds < 0 {
			return nil, "", "", fmt.Errorf("invalid $PGCONNECT_TIMEOUT value '%s': must be a non-negative integer (seconds): %w", envVars.PGCONNECT_TIMEOUT, pgmi.ErrInvalidConfig)
		}
		cfg.ConnectTimeout = time.Duration(seconds) * time.Second
	}

	// Maintenance database: pgmi.yaml > default ("postgres")
	maintenanceDB := pc.MaintenanceDatabase
	if maintenanceDB == "" {
		maintenanceDB = DefaultMaintenanceDB
	}

	return cfg, maintenanceDB, sslModeSource, nil
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %d: must be between 1 and 65535", port)
	}
	return nil
}
