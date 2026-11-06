package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBConnection is the slice of a connection pool that database management
// needs. CREATE DATABASE and DROP DATABASE cannot run in a transaction, so
// they go through Acquire on a dedicated connection.
type DBConnection interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	// QueryRow always returns a non-nil Row; errors surface from Scan.
	QueryRow(ctx context.Context, sql string, args ...any) Row
	// Acquire returns a connection the caller must Release.
	Acquire(ctx context.Context) (PooledConnection, error)
}

type Row interface {
	Scan(dest ...any) error
}

type PooledConnection interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Release()
}

// PoolAdapter adapts *pgxpool.Pool to DBConnection.
//
// Thread-Safety: Safe for concurrent use (pgxpool.Pool is thread-safe).
type PoolAdapter struct {
	pool *pgxpool.Pool
}

// NewPoolAdapter creates a new PoolAdapter wrapping the given pool.
func NewPoolAdapter(pool *pgxpool.Pool) *PoolAdapter {
	return &PoolAdapter{pool: pool}
}

// Exec executes a query without returning any rows.
func (p *PoolAdapter) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.pool.Exec(ctx, sql, args...)
}

// QueryRow executes a query that is expected to return at most one row.
func (p *PoolAdapter) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return &rowAdapter{row: p.pool.QueryRow(ctx, sql, args...)}
}

// Acquire obtains a dedicated connection from the pool.
func (p *PoolAdapter) Acquire(ctx context.Context) (PooledConnection, error) {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &pooledConnAdapter{conn: conn}, nil
}

// rowAdapter adapts pgx.Row to implement Row.
type rowAdapter struct {
	row interface{ Scan(...any) error }
}

// Scan reads the values from the row into dest values.
func (r *rowAdapter) Scan(dest ...any) error {
	return r.row.Scan(dest...)
}

// pooledConnAdapter adapts *pgxpool.Conn to implement PooledConnection.
type pooledConnAdapter struct {
	conn *pgxpool.Conn
}

// Exec executes a query on this specific connection.
func (p *pooledConnAdapter) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.conn.Exec(ctx, sql, args...)
}

// QueryRow runs a query expected to return at most one row on this connection.
func (p *pooledConnAdapter) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return &rowAdapter{row: p.conn.QueryRow(ctx, sql, args...)}
}

// Release returns the connection to the pool.
func (p *pooledConnAdapter) Release() {
	p.conn.Release()
}

// Verify PoolAdapter implements DBConnection at compile time
var _ DBConnection = (*PoolAdapter)(nil)

// Pinned is a DBConnection that runs everything on one held connection:
// Acquire hands it out again and its Release leaves it held.
func Pinned(conn PooledConnection) DBConnection {
	return pinned{conn}
}

type pinned struct{ PooledConnection }

func (p pinned) Acquire(context.Context) (PooledConnection, error) {
	return unreleased(p), nil
}

type unreleased struct{ PooledConnection }

func (unreleased) Release() {}
