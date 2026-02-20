package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/retry"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Connection pool configuration constants
const (
	// DefaultMaxConns limits concurrent connections to prevent resource exhaustion
	// during long-running deployments.
	DefaultMaxConns = 5

	// DefaultMinConns maintains at least one connection in the pool.
	DefaultMinConns = 1

	// DefaultMaxConnIdleTime keeps connections alive during long deployments
	// to avoid reconnection overhead.
	DefaultMaxConnIdleTime = 30 * time.Minute
)

func configurePool(poolConfig *pgxpool.Config) {
	poolConfig.MaxConns = DefaultMaxConns
	poolConfig.MinConns = DefaultMinConns
	poolConfig.MaxConnIdleTime = DefaultMaxConnIdleTime
	poolConfig.ConnConfig.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) {
		fmt.Println(notice.Message)
	}
}

// StandardConnector implements the Connector interface for standard
// username/password authentication with automatic retry on transient failures.
type StandardConnector struct {
	config        *pgmi.ConnectionConfig
	retryExecutor *retry.Executor
}

// NewStandardConnector creates a new StandardConnector with the given configuration.
// Retry behavior uses pgmi defaults: DefaultRetryMaxAttempts attempts,
// exponential backoff starting at DefaultRetryInitialDelay, max DefaultRetryMaxDelay.
func NewStandardConnector(config *pgmi.ConnectionConfig) *StandardConnector {
	classifier := retry.NewPostgreSQLErrorClassifier()
	strategy := retry.NewExponentialBackoff(pgmi.DefaultRetryMaxAttempts,
		retry.WithInitialDelay(pgmi.DefaultRetryInitialDelay),
		retry.WithMaxDelay(pgmi.DefaultRetryMaxDelay),
	)

	executor := retry.NewExecutor(classifier, strategy, nil)

	return &StandardConnector{
		config:        config,
		retryExecutor: executor,
	}
}

// Connect establishes a connection pool using standard authentication with automatic retry.
func (c *StandardConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool
	connStr := BuildConnectionString(c.config)

	// Use retry executor to handle transient connection failures
	err := c.retryExecutor.Execute(ctx, func(ctx context.Context) error {
		poolConfig, err := pgxpool.ParseConfig(connStr)
		if err != nil {
			return fmt.Errorf("failed to parse connection config: %w", err)
		}

		configurePool(poolConfig)

		pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			return wrapConnectionError(err, c.config.Host, c.config.Port, c.config.Database)
		}

		// Test the connection
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			return wrapConnectionError(err, c.config.Host, c.config.Port, c.config.Database)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return pool, nil
}

// NewConnector is a factory function that creates the appropriate Connector
// based on the ConnectionConfig's AuthMethod.
func NewConnector(config *pgmi.ConnectionConfig) (pgmi.Connector, error) {
	switch config.AuthMethod {
	case pgmi.AuthMethodStandard:
		return NewStandardConnector(config), nil
	case pgmi.AuthMethodAWSIAM:
		return newAWSConnector(config)
	case pgmi.AuthMethodGoogleIAM:
		return newGoogleConnector(config)
	case pgmi.AuthMethodAzureEntraID:
		return newAzureConnector(config)
	default:
		return nil, fmt.Errorf("unsupported auth method %v: %w", config.AuthMethod, pgmi.ErrUnsupportedAuthMethod)
	}
}

// wrapConnectionError wraps raw pgx connection errors with actionable guidance.
func wrapConnectionError(err error, host string, port int, database string) error {
	errStr := strings.ToLower(err.Error())
	addr := fmt.Sprintf("%s:%d", host, port)

	switch {
	case strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "actively refused"):
		return fmt.Errorf(`connection refused to %s

Possible causes:
  - PostgreSQL is not running (check: pg_isready -h %s -p %d)
  - Wrong host or port
  - Firewall blocking the connection

Original error: %w`, addr, host, port, err)

	case strings.Contains(errStr, "no such host") || strings.Contains(errStr, "no host"):
		return fmt.Errorf(`cannot resolve host "%s"

Possible causes:
  - Hostname is misspelled
  - DNS is not configured or reachable
  - Network connection issue

Original error: %w`, host, err)

	case strings.Contains(errStr, "password authentication failed"):
		return fmt.Errorf(`password authentication failed for database "%s"

Possible causes:
  - Wrong password (check $PGPASSWORD or ~/.pgpass)
  - Wrong username
  - User does not have access to the database

Original error: %w`, database, err)

	case strings.Contains(errStr, "does not exist"):
		return fmt.Errorf(`database "%s" does not exist

To create it:
  createdb %s

Or use --overwrite to let pgmi create it.

Original error: %w`, database, database, err)

	case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "timed out"):
		return fmt.Errorf(`connection timed out to %s

Possible causes:
  - Server is overloaded or unresponsive
  - Network latency or packet loss
  - Firewall silently dropping packets
  - Wrong host/port (server not listening)

Original error: %w`, addr, err)

	case strings.Contains(errStr, "ssl") || strings.Contains(errStr, "tls"):
		return fmt.Errorf(`SSL/TLS connection error

Possible causes:
  - Server requires SSL but --sslmode is wrong
  - Certificate verification failed (try --sslmode=require)
  - Client certificates missing (check --sslcert, --sslkey)

Original error: %w`, err)

	case strings.Contains(errStr, "too many connections"):
		return fmt.Errorf(`too many connections to database "%s"

Possible causes:
  - Connection pool exhausted on server
  - max_connections limit reached in postgresql.conf
  - Stale connections from previous deployments

Try: SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s';

Original error: %w`, database, database, err)

	default:
		return fmt.Errorf("failed to connect to database: %w", err)
	}
}

// newAWSConnector creates a token-based connector with the AWS IAM token provider.
func newAWSConnector(config *pgmi.ConnectionConfig) (pgmi.Connector, error) {
	endpoint := fmt.Sprintf("%s:%d", config.Host, config.Port)

	tokenProvider, err := NewAWSIAMTokenProvider(endpoint, config.AWSRegion, config.Username)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS IAM token provider: %w", err)
	}

	return NewTokenBasedConnector(config, tokenProvider, "AWS IAM"), nil
}

// newGoogleConnector creates a GoogleCloudSQLConnector for Google Cloud SQL IAM authentication.
func newGoogleConnector(config *pgmi.ConnectionConfig) (pgmi.Connector, error) {
	if config.GoogleInstance == "" {
		return nil, fmt.Errorf("Google Cloud SQL IAM auth requires --google-instance (project:region:instance)")
	}
	if config.Username == "" {
		return nil, fmt.Errorf("Google Cloud SQL IAM auth requires username (-U)")
	}

	return NewGoogleCloudSQLConnector(config, config.GoogleInstance), nil
}

// newAzureConnector creates a token-based connector with the Azure Entra ID token provider.
// If explicit credentials (tenant, client, secret) are provided, uses Service Principal auth.
// Otherwise, falls back to DefaultAzureCredential chain.
func newAzureConnector(config *pgmi.ConnectionConfig) (pgmi.Connector, error) {
	var tokenProvider TokenProvider
	var err error

	if config.AzureTenantID != "" && config.AzureClientID != "" && config.AzureClientSecret != "" {
		tokenProvider, err = NewAzureServicePrincipalProvider(
			config.AzureTenantID,
			config.AzureClientID,
			config.AzureClientSecret,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure Service Principal provider: %w", err)
		}
	} else {
		tokenProvider, err = NewAzureDefaultCredentialProvider()
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure Default Credential provider: %w", err)
		}
	}

	return NewTokenBasedConnector(config, tokenProvider, "Azure"), nil
}
