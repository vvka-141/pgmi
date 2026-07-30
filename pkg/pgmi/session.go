package pgmi

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionPreparer abstracts session preparation for testability.
//
// ScanProject is separate from PrepareSession so a caller can validate the
// project before touching the server: a project that cannot be scanned must
// not leave a freshly created database behind.
type SessionPreparer interface {
	ScanProject(sourcePath string) (FileScanResult, error)
	PrepareSession(ctx context.Context, connConfig *ConnectionConfig, scanResult FileScanResult, parameters map[string]string, compat string, verbose bool) (*Session, error)
}

// ReleaseDeployLock drops the deploy advisory lock before the connection goes
// back to the pool. Every path that acquires the lock must call it — both
// Session.Close and any failure that abandons a session mid-preparation.
//
// A tail unit can fail with an explicit BEGIN still open, which leaves the
// session aborted: every statement then returns 25P02, so the unlock silently
// does nothing, and pgxpool destroys a connection it finds outside TxStatus 'I'
// rather than returning it. The lock would survive until the server processed
// the disconnect — the asynchronous behavior this unlock exists to avoid. The
// rollback is conditional because a bare ROLLBACK on an idle connection raises
// a warning, and deploy notices are user-facing output.
func ReleaseDeployLock(ctx context.Context, conn *pgxpool.Conn) {
	if conn.Conn().PgConn().TxStatus() != 'I' {
		_, _ = conn.Exec(ctx, "ROLLBACK")
	}
	_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock_all()")
}

// Session holds database resources for a deployment. Not safe for concurrent use.
type Session struct {
	pool    *pgxpool.Pool
	conn    *pgxpool.Conn
	onClose func()

	FilesLoaded int
}

// NewSession creates a new Session. Panics if pool or conn is nil.
func NewSession(pool *pgxpool.Pool, conn *pgxpool.Conn, onClose func()) *Session {
	if pool == nil {
		panic("pool cannot be nil")
	}
	if conn == nil {
		panic("conn cannot be nil")
	}

	return &Session{
		pool:    pool,
		conn:    conn,
		onClose: onClose,
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
		// Release the deploy advisory lock synchronously before returning the
		// connection. pool.Close() below tears the connection down client-side,
		// but the server drops a session-scoped advisory lock only once it has
		// processed the disconnect — which a fast redeploy against the same
		// database can outrace, spuriously hitting ErrConcurrentDeploy. The
		// explicit unlock is a round-trip that completes before the next acquire.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ReleaseDeployLock(ctx, s.conn)
		cancel()

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
