package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/retry"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TokenBasedConnector implements the Connector interface for cloud providers
// that authenticate via short-lived tokens (AWS IAM, Azure Entra ID).
// The token is acquired from a TokenProvider and used as the PostgreSQL password.
type TokenBasedConnector struct {
	config        *ConnectionConfig
	tokenProvider TokenProvider
	retryExecutor *retry.Executor
	providerName  string
	out           Output
}

// NewTokenBasedConnector creates a connector that uses a TokenProvider for authentication.
// providerName is used in error/warning messages (e.g., "AWS IAM", "Azure").
func NewTokenBasedConnector(config *ConnectionConfig, tokenProvider TokenProvider, providerName string, out Output) *TokenBasedConnector {
	return &TokenBasedConnector{
		config:        config,
		tokenProvider: tokenProvider,
		retryExecutor: newConnectExecutor(out),
		providerName:  providerName,
		out:           out,
	}
}

func (c *TokenBasedConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool

	err := c.retryExecutor.Execute(ctx, func(ctx context.Context) error {
		// Initial token — also doubles as a reachability check before pool construction.
		token, expiresOn, err := c.tokenProvider.GetToken(ctx)
		if err != nil {
			return &connError{msg: fmt.Sprintf("failed to acquire %s token: %v", c.providerName, err), err: err}
		}

		if time.Until(expiresOn) < 5*time.Minute {
			c.out.info("Warning: %s token expires in %v", c.providerName, time.Until(expiresOn).Round(time.Second))
		}

		configWithToken := *c.config
		configWithToken.Password = token

		connStr := BuildConnectionString(&configWithToken)

		poolConfig, err := pgxpool.ParseConfig(connStr)
		if err != nil {
			return fmt.Errorf("%w: connection settings: %w", pgmi.ErrInvalidConfig, err)
		}

		configurePool(poolConfig, c.out)

		// Cloud auth tokens are short-lived (AWS RDS IAM: 15 min; Azure/GCP
		// ~1h). Every time pgx dials a NEW backend (initial fill, growth,
		// replacement after idle timeout), BeforeConnect acquires a fresh
		// token so deployments longer than the token TTL keep working.
		// The token baked into connStr above is only used as a fallback
		// when BeforeConnect errors — in practice it's overwritten below.
		tokenProvider := c.tokenProvider
		providerName := c.providerName
		poolConfig.BeforeConnect = func(ctx context.Context, cc *pgx.ConnConfig) error {
			freshToken, _, tokenErr := tokenProvider.GetToken(ctx)
			if tokenErr != nil {
				return fmt.Errorf("failed to refresh %s token on dial: %w", providerName, tokenErr)
			}
			cc.Password = freshToken
			return nil
		}

		// Cap connection lifetime to the remaining token validity so pgx
		// recycles backends before their password silently expires. Belt
		// and braces — BeforeConnect already covers the dial path, but a
		// still-open connection whose token expired mid-session can see
		// server-initiated auth churn on re-auth.
		if ttl := time.Until(expiresOn); ttl > 0 && ttl < poolConfig.MaxConnLifetime {
			poolConfig.MaxConnLifetime = ttl
		}

		pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			return WrapConnectionError(err, c.config.AuthMethod, c.config.Host, c.config.Port, c.config.Database, c.config.Username)
		}

		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			pool = nil
			return WrapConnectionError(err, c.config.AuthMethod, c.config.Host, c.config.Port, c.config.Database, c.config.Username)
		}

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
