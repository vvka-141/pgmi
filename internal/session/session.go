// Package session holds the database connection a deploy runs on.
package session

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

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
	Ignored     []pgmi.IgnoredPath
}

// New creates a Session. Panics if pool or conn is nil.
func New(pool *pgxpool.Pool, conn *pgxpool.Conn, onClose func()) *Session {
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
	grace := poolCloseGrace
	if s.conn != nil {
		if s.conn.Conn().IsClosed() {
			// The connection broke mid-query (a timeout or a cancel) and pgx
			// is draining it in the background. The server session, and the
			// deploy lock with it, ends when the server finishes or notices the
			// disconnect; waiting for the drain does not change when.
			grace = 0
		} else {
			// Release the deploy advisory lock synchronously before returning
			// the connection. pool.Close() below tears the connection down
			// client-side, but the server drops a session-scoped advisory lock
			// only once it has processed the disconnect — which a fast redeploy
			// against the same database can outrace, spuriously hitting
			// ErrConcurrentDeploy. The explicit unlock is a round-trip that
			// completes before the next acquire.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			ReleaseDeployLock(ctx, s.conn)
			cancel()
		}
		s.conn.Release()
		s.conn = nil
	}

	if s.pool != nil {
		closePool(s.pool, grace)
		s.pool = nil
	}

	if s.onClose != nil {
		s.onClose()
		s.onClose = nil
	}

	return nil
}

// poolCloseGrace bounds how long Close waits for the pool. pgx drains a
// connection that broke mid-query for up to 15 s before closing it, and after
// a timeout whose cancel request never reached the server, the server is still
// running the statement and sends nothing: the exit would wait the full 15 s.
// A healthy connection closes in a round trip, well inside the bound.
const poolCloseGrace = 2 * time.Second

func closePool(pool *pgxpool.Pool, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		pool.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
	}
}
