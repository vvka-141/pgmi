package pgmi

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionPreparer abstracts session preparation for testability.
type SessionPreparer interface {
	PrepareSession(ctx context.Context, connConfig *ConnectionConfig, sourcePath string, parameters map[string]string, compat string, verbose bool) (*Session, error)
}

// Session holds database resources for a deployment. Not safe for concurrent use.
type Session struct {
	pool       *pgxpool.Pool
	conn       *pgxpool.Conn
	scanResult FileScanResult
	onClose    func()
}

// NewSession creates a new Session. Panics if pool or conn is nil.
func NewSession(pool *pgxpool.Pool, conn *pgxpool.Conn, scanResult FileScanResult, onClose func()) *Session {
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
		onClose:    onClose,
	}
}

func (s *Session) Pool() *pgxpool.Pool {
	return s.pool
}

func (s *Session) Conn() *pgxpool.Conn {
	return s.conn
}

// Close releases all session resources. Idempotent.
func (s *Session) Close() error {
	if s.conn != nil {
		s.conn.Release()
		s.conn = nil
	}

	if s.pool != nil {
		s.pool.Close()
		s.pool = nil
	}

	if s.onClose != nil {
		s.onClose()
		s.onClose = nil
	}

	return nil
}
