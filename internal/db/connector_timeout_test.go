package db

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
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

	config := &ConnectionConfig{
		Host:     host,
		Port:     port,
		Database: "testdb",
		Username: "testuser",
		Password: "testpass",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()

	pool, err := NewStandardConnector(config, Output{}).Connect(ctx)
	returned := time.Now()

	if pool != nil {
		pool.Close()
	}
	if err == nil {
		t.Fatal("expected a connection error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error must carry context.DeadlineExceeded, got: %v", err)
	}

	// Lower bound only, against the context's own deadline: a stopwatch started
	// after WithTimeout runs behind the deadline and can read just under it.
	if returned.Before(deadline) {
		t.Errorf("returned %v before the deadline — the context was not what stopped it", deadline.Sub(returned))
	}
}
