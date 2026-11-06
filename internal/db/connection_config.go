package db

import (
	"fmt"
	"maps"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// ConnectionConfig represents parsed connection parameters.
type ConnectionConfig struct {
	Host     string
	Port     int
	Database string
	Username string
	Password string
	SSLMode  string

	// AuthMethod indicates the authentication mechanism to use
	AuthMethod pgmi.AuthMethod

	// Additional connection parameters
	AppName          string
	ConnectTimeout   time.Duration
	AdditionalParams map[string]string

	// Azure Entra ID authentication parameters (used when AuthMethod is AuthMethodAzureEntraID)
	// If all three are provided, Service Principal authentication is used.
	// If none are provided, DefaultAzureCredential chain is used (env vars, managed identity, CLI, etc.)
	AzureTenantID     string
	AzureClientID     string
	AzureClientSecret string

	// AWS IAM authentication parameters (used when AuthMethod is AuthMethodAWSIAM)
	// Region is required for RDS IAM authentication token generation.
	AWSRegion string

	// Google Cloud SQL IAM authentication parameters (used when AuthMethod is AuthMethodGoogleIAM)
	// GoogleInstance is the instance connection name in format: project:region:instance
	GoogleInstance string

	// TLS client certificate parameters (transport-layer, not auth method)
	// pgx handles mTLS natively via these connection string parameters
	SSLCert     string
	SSLKey      string
	SSLRootCert string
	SSLPassword string
}

// String renders the config with its three secrets — Password,
// AzureClientSecret and SSLPassword — replaced, and reports only whether each
// was set.
//
// Without it, one %v in a log line, a wrapped error or a panic dump prints all
// three in clear, and redactPasswords does not catch that: it looks for
// `password=` or a URI, while a struct renders `Password:hunter2` under %+v and
// a bare positional value under %v. A value receiver so both a
// ConnectionConfig and a *ConnectionConfig format safely.
func (c ConnectionConfig) String() string {
	return fmt.Sprintf(
		"ConnectionConfig{Host:%s Port:%d Database:%s Username:%s SSLMode:%s AuthMethod:%d "+
			"Password:%s AzureClientSecret:%s SSLPassword:%s}",
		c.Host, c.Port, c.Database, c.Username, c.SSLMode, c.AuthMethod,
		secretState(c.Password), secretState(c.AzureClientSecret), secretState(c.SSLPassword))
}

// GoString covers %#v, which ignores String and would otherwise dump every
// field verbatim.
func (c ConnectionConfig) GoString() string { return c.String() }

func secretState(s string) string {
	if s == "" {
		return "unset"
	}
	return "[redacted]"
}

// DeepCopy returns a deep copy of the ConnectionConfig.
func (c *ConnectionConfig) DeepCopy() ConnectionConfig {
	cp := *c
	if c.AdditionalParams != nil {
		cp.AdditionalParams = maps.Clone(c.AdditionalParams)
	}
	return cp
}
