package db

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/session"
)

// When the cancel request cannot reach the server, the socket deadline still
// ends the query once the grace runs out.
func TestCancelOnServer_UnreachableCancelFallsBackToTheSocketDeadline(t *testing.T) {
	connString := requireTokenTestDB(t)
	poolConfig, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatal(err)
	}
	configurePool(poolConfig, Output{})
	dial := poolConfig.ConnConfig.DialFunc
	var connected atomic.Bool
	var blocked atomic.Int32
	poolConfig.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if connected.Load() {
			blocked.Add(1)
			return nil, errors.New("cancel request blocked by test")
		}
		return dial(ctx, network, addr)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	connected.Store(true)

	const timeout = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	_, err = conn.Exec(ctx, "SELECT pg_sleep(30)")
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want the context deadline, got %v", err)
	}
	if elapsed > timeout+cancelGrace+time.Second {
		t.Errorf("query stopped after %s; want within %s plus the %s grace", elapsed, timeout, cancelGrace)
	}

	// The exit waits for the pool too: pgx 5.12 drains a broken connection for
	// up to 15 s before closing it, while the server still runs the statement
	// whose cancel never arrived.
	if err := session.New(pool, conn, nil).Close(); err != nil {
		t.Fatal(err)
	}
	if total := time.Since(start); total > timeout+cancelGrace+time.Second {
		t.Errorf("query and pool close took %s; want within %s plus the %s grace", total, timeout, cancelGrace)
	}
	if blocked.Load() == 0 {
		t.Error("no cancel request was attempted")
	}
}
