package pgmi_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	pgmitesting "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// A tail unit can fail with an explicit BEGIN still open, which leaves the
// session in aborted-transaction state. Every statement then returns 25P02, so
// the unlock that Close() relies on silently does nothing, and pgxpool destroys
// the connection instead of returning it — releasing the advisory lock only
// when the server processes the disconnect. That is the asynchronous behavior
// PGMI-209 removed, and it makes an immediate re-deploy fail with exit 15.
//
// Asserting on the lock from a second session would be a race: the disconnect
// usually releases it within microseconds, so such a test passes against the
// unfixed code. Both assertions here run on the aborted connection itself and
// are therefore timing-independent.
func TestReleaseDeployLock_ReleasesLockOnAbortedTransaction(t *testing.T) {
	pgmitesting.SkipIfShort(t)
	connString := pgmitesting.GetTestConnectionString(t)

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	const lockKey = 0x7067_6d69

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		t.Fatalf("take advisory lock: %v", err)
	}

	// Reproduce a failed tail transaction: an explicit BEGIN left open by a
	// statement that errors.
	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin tail transaction: %v", err)
	}
	if _, err := conn.Exec(ctx, "SELECT 1/0"); err == nil {
		t.Fatal("expected division by zero to fail")
	}
	if status := conn.Conn().PgConn().TxStatus(); status != 'E' {
		t.Fatalf("expected aborted transaction state 'E', got %q", status)
	}

	pgmi.ReleaseDeployLock(ctx, conn)

	// pgxpool destroys any connection it finds outside 'I' rather than
	// returning it to the pool, which is what defers the lock release.
	if status := conn.Conn().PgConn().TxStatus(); status != 'I' {
		t.Fatalf("connection left in TxStatus %q after cleanup; pgxpool will destroy it "+
			"instead of returning it, so the deploy lock is released only when the server "+
			"processes the disconnect and an immediate re-deploy can fail with exit 15", status)
	}

	var advisoryLocks int
	err = conn.QueryRow(ctx,
		"SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid()",
	).Scan(&advisoryLocks)
	if err != nil {
		t.Fatalf("count advisory locks: %v", err)
	}
	if advisoryLocks != 0 {
		t.Errorf("advisory lock still held after cleanup (%d held); the deploy lock "+
			"outlives the session that took it", advisoryLocks)
	}
}

// The rollback must not fire on a healthy connection: a bare ROLLBACK outside a
// transaction raises a warning, and deploy notices are user-facing output.
func TestReleaseDeployLock_IdleConnectionStaysSilent(t *testing.T) {
	pgmitesting.SkipIfShort(t)
	connString := pgmitesting.GetTestConnectionString(t)

	ctx := context.Background()

	var notices []string
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	config.ConnConfig.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		notices = append(notices, n.Message)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	pgmi.ReleaseDeployLock(ctx, conn)

	if len(notices) != 0 {
		t.Errorf("cleanup on an idle connection emitted notices %v; these reach the "+
			"user as deploy output", notices)
	}
}
