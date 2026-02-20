package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/retry"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TokenBasedConnector implements the Connector interface for cloud providers
// that authenticate via short-lived tokens (AWS IAM, Azure Entra ID).
// The token is acquired from a TokenProvider and used as the PostgreSQL password.
type TokenBasedConnector struct {
	config        *pgmi.ConnectionConfig
	tokenProvider TokenProvider
	retryExecutor *retry.Executor
	providerName  string
}

// NewTokenBasedConnector creates a connector that uses a TokenProvider for authentication.
// providerName is used in error/warning messages (e.g., "AWS IAM", "Azure").
func NewTokenBasedConnector(config *pgmi.ConnectionConfig, tokenProvider TokenProvider, providerName string) *TokenBasedConnector {
	classifier := retry.NewPostgreSQLErrorClassifier()
	strategy := retry.NewExponentialBackoff(pgmi.DefaultRetryMaxAttempts,
		retry.WithInitialDelay(pgmi.DefaultRetryInitialDelay),
		retry.WithMaxDelay(pgmi.DefaultRetryMaxDelay),
	)
	executor := retry.NewExecutor(classifier, strategy, nil)

	return &TokenBasedConnector{
		config:        config,
		tokenProvider: tokenProvider,
		retryExecutor: executor,
		providerName:  providerName,
	}
}

func (c *TokenBasedConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool

	err := c.retryExecutor.Execute(ctx, func(ctx context.Context) error {
		token, expiresOn, err := c.tokenProvider.GetToken(ctx)
		if err != nil {
			return fmt.Errorf("failed to acquire %s token: %w", c.providerName, err)
		}

		if time.Until(expiresOn) < 5*time.Minute {
			fmt.Printf("Warning: %s token expires in %v\n", c.providerName, time.Until(expiresOn).Round(time.Second))
		}

		configWithToken := *c.config
		configWithToken.Password = token

		connStr := BuildConnectionString(&configWithToken)

		poolConfig, err := pgxpool.ParseConfig(connStr)
		if err != nil {
			return fmt.Errorf("failed to parse connection config: %w", err)
		}

		configurePool(poolConfig)

		pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			return wrapConnectionError(err, c.config.Host, c.config.Port, c.config.Database)
		}

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
