package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/retry"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Connection pool configuration constants
const (
	// DefaultMaxConns limits concurrent connections. pgmi uses exactly one session
	// connection; a second slot handles rare concurrent health checks.
	DefaultMaxConns = 2

	// DefaultMinConns avoids eagerly dialing throwaway connections.
	DefaultMinConns = 0

	// DefaultMaxConnIdleTime keeps connections alive during long deployments
	// to avoid reconnection overhead.
	DefaultMaxConnIdleTime = 30 * time.Minute
)

// NoticeHandler is called for each PostgreSQL NOTICE/WARNING during execution.
// Replaceable to support timing prefixes in verbose mode.
var NoticeHandler func(message, detail, hint string) = DefaultNoticeHandler

// DefaultNoticeHandler prints notices to stderr without decoration.
func DefaultNoticeHandler(message, detail, hint string) {
	fmt.Fprintln(os.Stderr, message)
	if detail != "" {
		fmt.Fprintf(os.Stderr, "DETAIL: %s\n", detail)
	}
	if hint != "" {
		fmt.Fprintf(os.Stderr, "HINT: %s\n", hint)
	}
}

func configurePool(poolConfig *pgxpool.Config) {
	poolConfig.MaxConns = DefaultMaxConns
	poolConfig.MinConns = DefaultMinConns
	poolConfig.MaxConnIdleTime = DefaultMaxConnIdleTime
	poolConfig.ConnConfig.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) {
		NoticeHandler(notice.Message, notice.Detail, notice.Hint)
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

	executor := retry.NewExecutor(classifier, strategy)

	return &StandardConnector{
		config:        config,
		retryExecutor: executor,
	}
}

// Connect establishes a connection pool using standard authentication with automatic retry.
func (c *StandardConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool
	connStr := BuildConnectionString(c.config)

	// Use retry executor to handle transient connection failures.
	// Every error path in the closure MUST null out `pool` so a prior
	// attempt's half-constructed pool is not returned alongside an error
	// and is not leaked across retry iterations.
	err := c.retryExecutor.Execute(ctx, func(ctx context.Context) error {
		poolConfig, err := pgxpool.ParseConfig(connStr)
		if err != nil {
			pool = nil
			return fmt.Errorf("failed to parse connection config: %w", err)
		}

		configurePool(poolConfig)

		newPool, err := pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			pool = nil
			return wrapConnectionError(err, c.config.Host, c.config.Port, c.config.Database)
		}

		// Test the connection
		if err := newPool.Ping(ctx); err != nil {
			newPool.Close()
			pool = nil
			return wrapConnectionError(err, c.config.Host, c.config.Port, c.config.Database)
		}

		pool = newPool
		return nil
	})

	if err != nil {
		// Defensive: if retries exhausted after a success-then-fail sequence,
		// make sure we don't hand back a stale pool.
		if pool != nil {
			pool.Close()
			pool = nil
		}
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

// connError keeps the user-visible message free of wrapped-error noise while
// still surfacing the raw err and the ErrConnectionFailed sentinel through
// errors.Is(). Without the custom Unwrap, we'd have to repeat err.Error() in
// the visible message to keep it unwrappable.
type connError struct {
	msg string
	err error
}

func (e *connError) Error() string  { return e.msg }
func (e *connError) Unwrap() []error { return []error{e.err, pgmi.ErrConnectionFailed} }

func newConnError(err error, format string, args ...any) error {
	return &connError{msg: fmt.Sprintf(format, args...), err: err}
}

// wrapConnectionError wraps raw pgx connection errors with actionable guidance.
// All returned errors satisfy errors.Is(err, pgmi.ErrConnectionFailed) and
// errors.Is(err, originalErr).
func wrapConnectionError(err error, host string, port int, database string) error {
	errStr := strings.ToLower(err.Error())
	addr := fmt.Sprintf("%s:%d", host, port)

	switch {
	case strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "actively refused"):
		return newConnError(err, "connection refused to %s\nis PostgreSQL running? check: pg_isready -h %s -p %d", addr, host, port)

	case strings.Contains(errStr, "no such host") || strings.Contains(errStr, "no host"):
		return newConnError(err, "cannot resolve host %q\ncheck hostname spelling, DNS, and $PGHOST", host)

	case strings.Contains(errStr, "password authentication failed"):
		return newConnError(err, "password authentication failed for database %q\ncheck $PGPASSWORD, ~/.pgpass, or the connection string", database)

	case strings.Contains(errStr, "does not exist"):
		return newConnError(err, "database %q does not exist\ncreate it with `createdb %s` or pass --overwrite to let pgmi create it", database, database)

	case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "timed out"):
		return newConnError(err, "connection timed out to %s\ncheck network reachability and server load; raise --timeout if needed", addr)

	case strings.Contains(errStr, "ssl") || strings.Contains(errStr, "tls"):
		return newConnError(err, "SSL/TLS connection error: %v\ncheck --sslmode, --sslcert, --sslkey, --sslrootcert", err)

	case strings.Contains(errStr, "too many connections"):
		return newConnError(err, "too many connections to database %q\nserver is at max_connections; close idle backends or wait", database)

	default:
		return newConnError(err, "failed to connect to database: %v", err)
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
