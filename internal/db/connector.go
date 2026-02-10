package db

import (
	"context"
	"fmt"
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

	// Use retry executor to handle transient connection failures
	err := c.retryExecutor.Execute(ctx, func(ctx context.Context) error {
		connStr := BuildConnectionString(c.config)

		poolConfig, err := pgxpool.ParseConfig(connStr)
		if err != nil {
			return fmt.Errorf("failed to parse connection config: %w", err)
		}

		// Set explicit pool limits for deployment operations to prevent resource exhaustion
		// during long-running root.sql orchestration
		poolConfig.MaxConns = DefaultMaxConns
		poolConfig.MinConns = DefaultMinConns
		poolConfig.MaxConnIdleTime = DefaultMaxConnIdleTime

		// Configure notice handler to stream PostgreSQL RAISE NOTICE/INFO/WARNING to stdout
		// This is critical for the PostgreSQL-first experience - users should see database output directly
		poolConfig.ConnConfig.OnNotice = func(pc *pgconn.PgConn, notice *pgconn.Notice) {
			fmt.Println(notice.Message)
		}

		pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			return fmt.Errorf("failed to connect to database: %w", err)
		}

		// Test the connection
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			return fmt.Errorf("failed to ping database: %w", err)
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

// newAWSConnector creates an AWSIAMConnector with the appropriate token provider.
func newAWSConnector(config *pgmi.ConnectionConfig) (pgmi.Connector, error) {
	// Build endpoint from host:port
	endpoint := fmt.Sprintf("%s:%d", config.Host, config.Port)

	tokenProvider, err := NewAWSIAMTokenProvider(endpoint, config.AWSRegion, config.Username)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS IAM token provider: %w", err)
	}

	return NewAWSIAMConnector(config, tokenProvider), nil
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

// newAzureConnector creates an AzureEntraIDConnector with the appropriate token provider.
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

	return NewAzureEntraIDConnector(config, tokenProvider), nil
}
