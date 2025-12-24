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

// AzureEntraIDConnector implements the Connector interface for Azure Entra ID authentication.
// It acquires an OAuth token from Azure AD and uses it as the PostgreSQL password.
type AzureEntraIDConnector struct {
	config        *pgmi.ConnectionConfig
	tokenProvider TokenProvider
	retryExecutor *retry.Executor
}

// NewAzureEntraIDConnector creates a connector for Azure Entra ID authentication.
// The tokenProvider is used to acquire OAuth tokens for PostgreSQL access.
func NewAzureEntraIDConnector(config *pgmi.ConnectionConfig, tokenProvider TokenProvider) *AzureEntraIDConnector {
	classifier := retry.NewPostgreSQLErrorClassifier()
	strategy := retry.NewExponentialBackoff(pgmi.DefaultRetryMaxAttempts,
		retry.WithInitialDelay(pgmi.DefaultRetryInitialDelay),
		retry.WithMaxDelay(pgmi.DefaultRetryMaxDelay),
	)

	executor := retry.NewExecutor(classifier, strategy, nil)

	return &AzureEntraIDConnector{
		config:        config,
		tokenProvider: tokenProvider,
		retryExecutor: executor,
	}
}

// Connect establishes a connection pool using Azure Entra ID authentication.
// The OAuth token is acquired and used as the password for the PostgreSQL connection.
func (c *AzureEntraIDConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool

	err := c.retryExecutor.Execute(ctx, func(ctx context.Context) error {
		// Acquire fresh token for each connection attempt
		token, expiresOn, err := c.tokenProvider.GetToken(ctx)
		if err != nil {
			return fmt.Errorf("failed to acquire Azure token: %w", err)
		}

		// Log token acquisition (without the token itself)
		if time.Until(expiresOn) < 5*time.Minute {
			fmt.Printf("Warning: Azure token expires in %v\n", time.Until(expiresOn).Round(time.Second))
		}

		// Create a copy of config with the token as password
		configWithToken := *c.config
		configWithToken.Password = token

		connStr := BuildConnectionString(&configWithToken)

		poolConfig, err := pgxpool.ParseConfig(connStr)
		if err != nil {
			return fmt.Errorf("failed to parse connection config: %w", err)
		}

		poolConfig.MaxConns = DefaultMaxConns
		poolConfig.MinConns = DefaultMinConns
		poolConfig.MaxConnIdleTime = DefaultMaxConnIdleTime

		poolConfig.ConnConfig.OnNotice = func(pc *pgconn.PgConn, notice *pgconn.Notice) {
			fmt.Println(notice.Message)
		}

		pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			return fmt.Errorf("failed to connect to database: %w", err)
		}

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
