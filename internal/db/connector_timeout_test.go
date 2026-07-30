package db

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// blackholeAddr returns an address that accepts TCP connections and then says
// nothing. pgx completes the dial and blocks waiting for the server's reply to
// the startup message, which is the only way to reach the context deadline
// deterministically — pointing the connector at an unresolvable host instead
// fails DNS in about 2ms and never exercises the timeout at all.
func blackholeAddr(t *testing.T) (host string, port int) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var mu sync.Mutex
	var accepted []net.Conn
	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted = append(accepted, conn)
			mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range accepted {
			_ = conn.Close()
		}
	})

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// TestStandardConnector_RespectsContextTimeout verifies that the connector respects
// the context timeout passed from the CLI.
func TestStandardConnector_RespectsContextTimeout(t *testing.T) {
	host, port := blackholeAddr(t)

	config := &pgmi.ConnectionConfig{
		Host:     host,
		Port:     port,
		Database: "testdb",
		Username: "testuser",
		Password: "testpass",
	}

	const timeout = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	pool, err := NewStandardConnector(config).Connect(ctx)
	elapsed := time.Since(start)

	if pool != nil {
		pool.Close()
	}
	if err == nil {
		t.Fatal("expected a connection error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error must carry context.DeadlineExceeded, got: %v", err)
	}

	// Lower bound only. An upper bound would be a latency measurement the
	// scheduler decides, and it has failed under concurrent load. Returning
	// *before* the deadline is the real defect: it means something other than
	// the context ended the attempt.
	if elapsed < timeout {
		t.Errorf("returned after %v, before the %v deadline — the context was not what stopped it", elapsed, timeout)
	}
}
