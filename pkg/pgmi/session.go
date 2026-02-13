package pgmi

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionPreparer abstracts session preparation for testability.
type SessionPreparer interface {
	PrepareSession(ctx context.Context, connConfig *ConnectionConfig, sourcePath string, parameters map[string]string, compat string, verbose bool) (*Session, error)
}

// Session encapsulates a prepared deployment session with database connection,
// pooled connection, and file scan results.
//
// Session manages the lifecycle of database resources (pool and connection)
// and ensures proper cleanup through a single Close() method.
//
// Thread-Safety: NOT safe for concurrent use. Each goroutine should have
// its own Session instance.
//
// Lifecycle:
//   1. Created by SessionManager.PrepareSession()
//   2. Used for deployment or testing operations
//   3. Cleaned up via Close() (idempotent)
//
// Example usage:
//
//	session, err := sessionManager.PrepareSession(ctx, config, path, params)
//	if err != nil {
//	    return err
//	}
//	defer session.Close()  // Single cleanup call - simple and safe
//
//	// Use session.Pool(), session.Conn(), session.ScanResult()
type Session struct {
	pool       *pgxpool.Pool
	conn       *pgxpool.Conn
	scanResult FileScanResult
}

// NewSession creates a new Session instance.
// This is intended to be called by SessionManager, not by external code.
//
// Panics if pool or conn is nil (programmer error - SessionManager
// should never create a Session with nil resources).
func NewSession(pool *pgxpool.Pool, conn *pgxpool.Conn, scanResult FileScanResult) *Session {
	if pool == nil {
		panic("pool cannot be nil")
	}
	if conn == nil {
		panic("conn cannot be nil")
	}

	return &Session{
		pool:       pool,
		conn:       conn,
		scanResult: scanResult,
	}
}

// Pool returns the connection pool for the session.
// The pool is valid until Close() is called.
func (s *Session) Pool() *pgxpool.Pool {
	return s.pool
}

// Conn returns the acquired pooled connection for the session.
// This connection is session-scoped and used for all pg_temp operations.
// The connection is valid until Close() is called.
func (s *Session) Conn() *pgxpool.Conn {
	return s.conn
}

// ScanResult returns the file scan results for the session.
// Includes all discovered files and placeholder metadata.
func (s *Session) ScanResult() FileScanResult {
	return s.scanResult
}

// Close releases all resources associated with the session.
// This method is idempotent and safe to call multiple times.
//
// Resource cleanup order:
//   1. Release the acquired connection back to the pool
//   2. Close the connection pool
//
// After calling Close(), the Session should not be used.
func (s *Session) Close() error {
	// Release connection first (if not nil)
	if s.conn != nil {
		s.conn.Release()
		s.conn = nil
	}

	// Close pool second (if not nil)
	if s.pool != nil {
		s.pool.Close()
		s.pool = nil
	}

	return nil
}
