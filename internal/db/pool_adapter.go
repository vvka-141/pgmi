package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// PoolAdapter adapts *pgxpool.Pool to implement the pgmi.DBConnection interface.
// This decouples the internal implementation from the public API, preventing
// direct exposure of pgx-specific types.
//
// Thread-Safety: Safe for concurrent use (pgxpool.Pool is thread-safe).
type PoolAdapter struct {
	pool *pgxpool.Pool
}

// NewPoolAdapter creates a new PoolAdapter wrapping the given pool.
func NewPoolAdapter(pool *pgxpool.Pool) pgmi.DBConnection {
	return &PoolAdapter{pool: pool}
}

// Exec executes a query without returning any rows.
func (p *PoolAdapter) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.pool.Exec(ctx, sql, args...)
}

// QueryRow executes a query that is expected to return at most one row.
func (p *PoolAdapter) QueryRow(ctx context.Context, sql string, args ...any) pgmi.Row {
	return &rowAdapter{row: p.pool.QueryRow(ctx, sql, args...)}
}

// Acquire obtains a dedicated connection from the pool.
func (p *PoolAdapter) Acquire(ctx context.Context) (pgmi.PooledConnection, error) {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &pooledConnAdapter{conn: conn}, nil
}

// rowAdapter adapts pgx.Row to implement pgmi.Row.
type rowAdapter struct {
	row interface{ Scan(...any) error }
}

// Scan reads the values from the row into dest values.
func (r *rowAdapter) Scan(dest ...any) error {
	return r.row.Scan(dest...)
}

// pooledConnAdapter adapts *pgxpool.Conn to implement pgmi.PooledConnection.
type pooledConnAdapter struct {
	conn *pgxpool.Conn
}

// Exec executes a query on this specific connection.
func (p *pooledConnAdapter) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.conn.Exec(ctx, sql, args...)
}

// Release returns the connection to the pool.
func (p *pooledConnAdapter) Release() {
	p.conn.Release()
}

// Verify PoolAdapter implements DBConnection at compile time
var _ pgmi.DBConnection = (*PoolAdapter)(nil)
