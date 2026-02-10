package db

import (
	"context"
	"fmt"
	"net"

	"cloud.google.com/go/cloudsqlconn"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// GoogleCloudSQLConnector implements the Connector interface for Google Cloud SQL
// using IAM database authentication via the Cloud SQL Go Connector.
type GoogleCloudSQLConnector struct {
	config   *pgmi.ConnectionConfig
	instance string
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
func (c *GoogleCloudSQLConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
	// Create Cloud SQL dialer with IAM authentication
	dialer, err := cloudsqlconn.NewDialer(ctx, cloudsqlconn.WithIAMAuthN())
	if err != nil {
		return nil, fmt.Errorf("failed to create Cloud SQL dialer: %w", err)
	}

	// Build DSN without password (IAM auth handles it) and with sslmode=disable
	// (Cloud SQL connector handles TLS internally)
	dsn := fmt.Sprintf(
		"host=%s user=%s dbname=%s sslmode=disable",
		c.instance, // Using instance as "host" - will be overridden by DialFunc
		c.config.Username,
		c.config.Database,
	)

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		dialer.Close()
		return nil, fmt.Errorf("failed to parse connection config: %w", err)
	}

	// Configure the custom dial function to use Cloud SQL connector
	poolConfig.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.Dial(ctx, c.instance)
	}

	poolConfig.MaxConns = DefaultMaxConns
	poolConfig.MinConns = DefaultMinConns
	poolConfig.MaxConnIdleTime = DefaultMaxConnIdleTime

	poolConfig.ConnConfig.OnNotice = func(pc *pgconn.PgConn, notice *pgconn.Notice) {
		fmt.Println(notice.Message)
	}

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

	return pool, nil
}
