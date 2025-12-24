package pgmi

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

// DBConnection abstracts database connection operations needed by DatabaseManager.
// This interface decouples the public API from pgx-specific types while providing
// the essential operations for database management.
//
// Thread-Safety: Implementations should follow their underlying connection's
// thread-safety guarantees. Connection pool implementations are typically safe
// for concurrent use.
type DBConnection interface {
	// Exec executes a query without returning any rows.
	// Returns CommandTag containing information about the query execution.
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)

	// QueryRow executes a query that is expected to return at most one row.
	// Always returns a non-nil Row. Errors are deferred until Row's Scan method is called.
	QueryRow(ctx context.Context, sql string, args ...any) Row

	// Acquire obtains a dedicated connection from the pool for operations
	// that require connection affinity (e.g., CREATE DATABASE, DROP DATABASE
	// which cannot run in transactions).
	// Caller must call Release() on the returned PooledConnection when done.
	Acquire(ctx context.Context) (PooledConnection, error)
}

// Row represents a single row returned by QueryRow.
// This interface decouples from pgx.Row.
type Row interface {
	// Scan reads the values from the row into dest values.
	// Returns an error if no row was found or if the scan fails.
	Scan(dest ...any) error
}

// PooledConnection represents a connection acquired from a pool.
// The caller must call Release() when done to return it to the pool.
type PooledConnection interface {
	// Exec executes a query on this specific connection.
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)

	// Release returns the connection to the pool.
	// After calling Release, the connection should not be used.
	Release()
}
