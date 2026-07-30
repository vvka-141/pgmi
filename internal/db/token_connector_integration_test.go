package db

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/testinfra"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var (
	tokenTestContainerOnce sync.Once
	tokenTestContainerConn string
	tokenTestContainerErr  error
)

func requireTokenTestDB(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if conn := os.Getenv("PGMI_TEST_CONN"); conn != "" {
		return conn
	}
	tokenTestContainerOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				tokenTestContainerErr = fmt.Errorf("testcontainer startup panicked: %v", r)
			}
		}()
		container, err := testinfra.StartSimplePostgres(context.Background())
		if err != nil {
			tokenTestContainerErr = err
			return
		}
		tokenTestContainerConn = container.ConnString
	})
	if tokenTestContainerErr == nil {
		return tokenTestContainerConn
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PGMI_REQUIRE_DB"))) {
	case "", "0", "false", "no":
		t.Skipf("PGMI_TEST_CONN not set and Docker unavailable: %v", tokenTestContainerErr)
	default:
		t.Fatalf("PGMI_REQUIRE_DB is set but no test database is available: %v", tokenTestContainerErr)
	}
	return ""
}

func parseTestConnConfig(t *testing.T, connStr string) *pgmi.ConnectionConfig {
	t.Helper()
	u, err := url.Parse(connStr)
	if err != nil {
		t.Fatalf("parse test connection string: %v", err)
	}
	port := 5432
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			t.Fatalf("parse port: %v", err)
		}
	}
	password, _ := u.User.Password()
	return &pgmi.ConnectionConfig{
		Host:     u.Hostname(),
		Port:     port,
		Database: strings.TrimPrefix(u.Path, "/"),
		Username: u.User.Username(),
		Password: password,
		SSLMode:  u.Query().Get("sslmode"),
	}
}

type countingTokenProvider struct {
	token     string
	expiresOn time.Time
	err       error
	calls     atomic.Int64
}

func (p *countingTokenProvider) GetToken(ctx context.Context) (string, time.Time, error) {
	p.calls.Add(1)
	if p.err != nil {
		return "", time.Time{}, p.err
	}
	return p.token, p.expiresOn, nil
}

func (p *countingTokenProvider) String() string { return "CountingTokenProvider" }

func TestTokenBasedConnector_Connect(t *testing.T) {
	connStr := requireTokenTestDB(t)
	realConfig := parseTestConnConfig(t, connStr)

	t.Run("happy path", func(t *testing.T) {
		provider := &MockTokenProvider{
			Token:     realConfig.Password,
			ExpiresOn: time.Now().Add(1 * time.Hour),
		}
		config := &pgmi.ConnectionConfig{
			Host:     realConfig.Host,
			Port:     realConfig.Port,
			Database: realConfig.Database,
			Username: realConfig.Username,
			SSLMode:  realConfig.SSLMode,
		}
		connector := NewTokenBasedConnector(config, provider, "Test")
		pool, err := connector.Connect(context.Background())
		if err != nil {
			t.Fatalf("Connect() error = %v", err)
		}
		defer pool.Close()

		var one int
		if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&one); err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if one != 1 {
			t.Errorf("expected 1, got %d", one)
		}
	})

	t.Run("token provider error surfaces", func(t *testing.T) {
		provider := &MockTokenProvider{
			Err: fmt.Errorf("credential expired"),
		}
		config := &pgmi.ConnectionConfig{
			Host:     realConfig.Host,
			Port:     realConfig.Port,
			Database: realConfig.Database,
			Username: realConfig.Username,
			SSLMode:  realConfig.SSLMode,
		}
		connector := NewTokenBasedConnector(config, provider, "Test")
		pool, err := connector.Connect(context.Background())
		if err == nil {
			pool.Close()
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "credential expired") {
			t.Errorf("error should contain provider message, got: %v", err)
		}
		if !strings.Contains(err.Error(), "Test token") {
			t.Errorf("error should mention provider name, got: %v", err)
		}
	})

	t.Run("token used on initial dial and BeforeConnect fires", func(t *testing.T) {
		provider := &countingTokenProvider{
			token:     realConfig.Password,
			expiresOn: time.Now().Add(1 * time.Hour),
		}
		config := &pgmi.ConnectionConfig{
			Host:     realConfig.Host,
			Port:     realConfig.Port,
			Database: realConfig.Database,
			Username: realConfig.Username,
			SSLMode:  realConfig.SSLMode,
		}
		connector := NewTokenBasedConnector(config, provider, "Test")
		pool, err := connector.Connect(context.Background())
		if err != nil {
			t.Fatalf("Connect() error = %v", err)
		}
		defer pool.Close()

		calls := provider.calls.Load()
		if calls < 2 {
			t.Errorf("expected at least 2 GetToken calls (initial + BeforeConnect dial), got %d", calls)
		}
	})

	t.Run("sub-five-minute expiry warning", func(t *testing.T) {
		provider := &MockTokenProvider{
			Token:     realConfig.Password,
			ExpiresOn: time.Now().Add(3 * time.Minute),
		}
		config := &pgmi.ConnectionConfig{
			Host:     realConfig.Host,
			Port:     realConfig.Port,
			Database: realConfig.Database,
			Username: realConfig.Username,
			SSLMode:  realConfig.SSLMode,
		}

		var buf bytes.Buffer
		origStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		connector := NewTokenBasedConnector(config, provider, "Test")
		pool, err := connector.Connect(context.Background())

		w.Close()
		os.Stderr = origStderr
		buf.ReadFrom(r)

		if err != nil {
			t.Fatalf("Connect() error = %v", err)
		}
		defer pool.Close()

		output := buf.String()
		if !strings.Contains(output, "Warning") || !strings.Contains(output, "Test token expires") {
			t.Errorf("expected expiry warning on stderr, got: %q", output)
		}
	})

	t.Run("MaxConnLifetime clamped to token TTL", func(t *testing.T) {
		shortTTL := 10 * time.Minute
		provider := &MockTokenProvider{
			Token:     realConfig.Password,
			ExpiresOn: time.Now().Add(shortTTL),
		}
		config := &pgmi.ConnectionConfig{
			Host:     realConfig.Host,
			Port:     realConfig.Port,
			Database: realConfig.Database,
			Username: realConfig.Username,
			SSLMode:  realConfig.SSLMode,
		}
		connector := NewTokenBasedConnector(config, provider, "Test")
		pool, err := connector.Connect(context.Background())
		if err != nil {
			t.Fatalf("Connect() error = %v", err)
		}
		defer pool.Close()

		stat := pool.Config()
		if stat.MaxConnLifetime > shortTTL {
			t.Errorf("MaxConnLifetime = %v, want <= %v", stat.MaxConnLifetime, shortTTL)
		}
	})
}
