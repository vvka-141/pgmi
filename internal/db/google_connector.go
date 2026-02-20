package db

import (
	"context"
	"fmt"
	"net"

	"cloud.google.com/go/cloudsqlconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// GoogleCloudSQLConnector implements the Connector interface for Google Cloud SQL
// using IAM database authentication via the Cloud SQL Go Connector.
//
// Implements io.Closer — caller must call Close() after the pool is closed
// to release the Cloud SQL dialer resources.
type GoogleCloudSQLConnector struct {
	config   *pgmi.ConnectionConfig
	instance string
	dialer   *cloudsqlconn.Dialer
}

// NewGoogleCloudSQLConnector creates a connector for Google Cloud SQL IAM authentication.
// instance is the instance connection name in format: project:region:instance
func NewGoogleCloudSQLConnector(config *pgmi.ConnectionConfig, instance string) *GoogleCloudSQLConnector {
	return &GoogleCloudSQLConnector{
		config:   config,
		instance: instance,
	}
}

// Connect establishes a connection pool using Google Cloud SQL IAM authentication.
// The Cloud SQL Go Connector handles authentication, TLS, and connection management.
//
// After the pool is closed, the caller must call Close() on this connector
// to release the Cloud SQL dialer.
func (c *GoogleCloudSQLConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
	dialer, err := cloudsqlconn.NewDialer(ctx, cloudsqlconn.WithIAMAuthN())
	if err != nil {
		return nil, fmt.Errorf("failed to create Cloud SQL dialer: %w", err)
	}

	dsn := fmt.Sprintf(
		"host=%s user=%s dbname=%s sslmode=disable",
		c.instance,
		c.config.Username,
		c.config.Database,
	)

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		dialer.Close()
		return nil, fmt.Errorf("failed to parse connection config: %w", err)
	}

	poolConfig.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.Dial(ctx, c.instance)
	}

	configurePool(poolConfig)

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		dialer.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		dialer.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	c.dialer = dialer
	return pool, nil
}

// Close releases the Cloud SQL dialer resources.
// Must be called after the connection pool returned by Connect() is closed.
func (c *GoogleCloudSQLConnector) Close() error {
	if c.dialer != nil {
		c.dialer.Close()
		c.dialer = nil
	}
	return nil
}
