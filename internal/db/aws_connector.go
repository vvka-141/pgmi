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

// AWSIAMConnector implements the Connector interface for AWS RDS IAM authentication.
// It acquires an IAM token from AWS and uses it as the PostgreSQL password.
type AWSIAMConnector struct {
	config        *pgmi.ConnectionConfig
	tokenProvider TokenProvider
	retryExecutor *retry.Executor
}

// NewAWSIAMConnector creates a connector for AWS RDS IAM authentication.
// The tokenProvider is used to acquire IAM authentication tokens.
func NewAWSIAMConnector(config *pgmi.ConnectionConfig, tokenProvider TokenProvider) *AWSIAMConnector {
	classifier := retry.NewPostgreSQLErrorClassifier()
	strategy := retry.NewExponentialBackoff(pgmi.DefaultRetryMaxAttempts,
		retry.WithInitialDelay(pgmi.DefaultRetryInitialDelay),
		retry.WithMaxDelay(pgmi.DefaultRetryMaxDelay),
	)

	executor := retry.NewExecutor(classifier, strategy, nil)

	return &AWSIAMConnector{
		config:        config,
		tokenProvider: tokenProvider,
		retryExecutor: executor,
	}
}

// Connect establishes a connection pool using AWS RDS IAM authentication.
// The IAM token is acquired and used as the password for the PostgreSQL connection.
func (c *AWSIAMConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool

	err := c.retryExecutor.Execute(ctx, func(ctx context.Context) error {
		// Acquire fresh token for each connection attempt
		token, expiresOn, err := c.tokenProvider.GetToken(ctx)
		if err != nil {
			return fmt.Errorf("failed to acquire AWS IAM token: %w", err)
		}

		// Log token acquisition (without the token itself)
		if time.Until(expiresOn) < 5*time.Minute {
			fmt.Printf("Warning: AWS IAM token expires in %v\n", time.Until(expiresOn).Round(time.Second))
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
