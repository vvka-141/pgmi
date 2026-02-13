package testing

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
)

// PoolWithNoticeCapture wraps a pgxpool.Pool with notice capture support.
type PoolWithNoticeCapture struct {
	*pgxpool.Pool
	Capture *NoticeCapture
}

// GetTestPoolWithNoticeCapture creates a connection pool with notice capture enabled.
// The pool is automatically closed when the test completes.
func GetTestPoolWithNoticeCapture(t *testing.T, connString, dbName string) *PoolWithNoticeCapture {
	t.Helper()

	ctx := context.Background()
	capture := NewNoticeCapture()

	// Parse connection string
	config, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatalf("Failed to parse connection string: %v", err)
	}

	// Override database name
	config.Database = dbName

	// Build connection string for target database
	targetConnString := db.BuildConnectionString(config)

	// Parse into pgxpool config
	poolConfig, err := pgxpool.ParseConfig(targetConnString)
	if err != nil {
		t.Fatalf("Failed to parse pool config: %v", err)
	}

	// Configure notice handler
	poolConfig.ConnConfig.OnNotice = capture.Handler()

	// Create pool
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("Failed to create connection pool: %v", err)
	}

	// Register cleanup
	t.Cleanup(func() {
		pool.Close()
	})

	return &PoolWithNoticeCapture{
		Pool:    pool,
		Capture: capture,
	}
}
