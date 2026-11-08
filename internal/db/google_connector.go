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
	config   *ConnectionConfig
	instance string
	dialer   *cloudsqlconn.Dialer
	out      Output
}

// NewGoogleCloudSQLConnector creates a connector for Google Cloud SQL IAM authentication.
// instance is the instance connection name in format: project:region:instance
func NewGoogleCloudSQLConnector(config *ConnectionConfig, instance string, out Output) *GoogleCloudSQLConnector {
	return &GoogleCloudSQLConnector{
		config:   config,
		instance: instance,
		out:      out,
	}
}

// Connect establishes a connection pool using Google Cloud SQL IAM authentication.
// The Cloud SQL Go Connector handles authentication, TLS, and connection management.
//
// After the pool is closed, the caller must call Close() on this connector
// to release the Cloud SQL dialer.
func (c *GoogleCloudSQLConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
	// Guard against double Connect(). A second call without Close() would
	// overwrite c.dialer, leaking the previous dialer's Cloud SQL auth
	// resources (background token refresh goroutines, TLS caches).
	if c.dialer != nil {
		return nil, fmt.Errorf("GoogleCloudSQLConnector.Connect: connector already connected; call Close() before reconnecting")
	}

	dialer, err := cloudsqlconn.NewDialer(ctx, cloudsqlconn.WithIAMAuthN())
	if err != nil {
		return nil, fmt.Errorf("failed to create Cloud SQL dialer: %w", err)
	}

	poolConfig, err := c.poolConfig()
	if err != nil {
		dialer.Close()
		return nil, err
	}

	poolConfig.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.Dial(ctx, c.instance)
	}

	configurePool(poolConfig, c.out)

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		dialer.Close()
		return nil, WrapConnectionError(err, c.config.AuthMethod, c.instance, c.config.Port, c.config.Database, c.config.Username)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		dialer.Close()
		return nil, WrapConnectionError(err, c.config.AuthMethod, c.instance, c.config.Port, c.config.Database, c.config.Username)
	}

	c.dialer = dialer
	return pool, nil
}

// poolConfig sets host, user and database as values, never by formatting
// them into a keyword string, where a name holding a space and "dbname="
// would choose another database. The dialer brings TLS, so sslmode=disable.
func (c *GoogleCloudSQLConnector) poolConfig() (*pgxpool.Config, error) {
	poolConfig, err := pgxpool.ParseConfig("sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("%w: connection settings: %w", pgmi.ErrInvalidConfig, err)
	}
	poolConfig.ConnConfig.Host = c.instance
	poolConfig.ConnConfig.User = c.config.Username
	poolConfig.ConnConfig.Database = c.config.Database
	return poolConfig, nil
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
