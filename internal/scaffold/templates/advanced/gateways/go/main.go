// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library. Namespaces, modules and package names are
// examples: rename them to match the project and add its specifics freely.
//
//	DATABASE_URL=postgresql://gateway:secret@localhost:5432/mydb go run .
//
// main is wiring only: read the environment (../PROTOCOL.md,
// "Configuration"; the same names in every reference gateway), build the
// pool, the channel stack, the interceptor pipeline, sign-in and the resource
// server once, and serve. Everything built here is shared, read-only, by
// every request goroutine.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const acquireTimeout = 5 * time.Second

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	port := getenv("PORT", "8080")
	addr := net.JoinHostPort(getenv("HOST", "127.0.0.1"), port)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	poolSize, err := getenvNumber("POOL_MAX_SIZE", 10)
	if err != nil {
		return err
	}
	pool, err := NewPool(ctx, databaseURL, int32(poolSize))
	if err != nil {
		return err
	}
	defer pool.Close()
	db := &Postgres{Pool: pool, AcquireTimeout: acquireTimeout}

	attempts, err := getenvNumber("RETRY_MAX_ATTEMPTS", 3)
	if err != nil {
		return err
	}
	baseDelay, err := getenvNumber("RETRY_BASE_DELAY", 0.05)
	if err != nil {
		return err
	}
	// ADAPT: the decorators your gateway needs, in one place. Both retries
	// wrap the tracing, so each attempt is a log line of its own.
	channel := ConnectionRetry{Attempts: 2, BaseDelay: 50 * time.Millisecond, Log: log,
		Channel: SerializationRetry{Attempts: int(attempts), BaseDelay: time.Duration(baseDelay * float64(time.Second)),
			Channel: Tracing{Log: log, Channel: db}}}

	pipeline, err := buildPipeline(splitList(os.Getenv("INTERCEPTORS"), ","), getenv("EMBED_FAILURE_POLICY", "fail"), log)
	if err != nil {
		return err
	}

	// One client for every outgoing call (the identity provider today, an
	// embedding API tomorrow): it pools connections and is safe to share.
	client := &http.Client{Timeout: 10 * time.Second}

	secret := []byte(os.Getenv("LOGIN_CSRF_SECRET"))
	if len(secret) == 0 {
		secret = []byte(rand.Text()) // per process: set it when several processes serve the same users
	}
	origins := map[string]bool{}
	for _, o := range splitList(getenv("ALLOWED_ORIGINS", "http://localhost:"+port+",http://127.0.0.1:"+port), ",") {
		origins[o] = true
	}

	g := &Gateway{Channel: channel, Pipeline: pipeline, DB: db, Sessions: Sessions{CSRFSecret: secret},
		AllowedOrigins: origins, DevLogin: os.Getenv("DEV_LOGIN") == "1", Log: log}

	if issuer := os.Getenv("OIDC_ISSUER"); issuer != "" {
		g.Login, err = NewOIDCLogin(OIDCConfig{Issuer: issuer, ClientID: os.Getenv("OIDC_CLIENT_ID"),
			ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"), RedirectURI: os.Getenv("OIDC_REDIRECT_URI"),
			Scopes: strings.Fields(getenv("OIDC_SCOPES", "openid email profile"))}, client)
		if err != nil {
			return err
		}
	}
	if resource := os.Getenv("MCP_RESOURCE"); resource != "" {
		g.Resource, err = NewResourceServer(resource, splitList(os.Getenv("MCP_AUTHORIZATION_SERVERS"), ","),
			strings.Fields(getenv("MCP_SCOPES", "openid")), os.Getenv("OIDC_CLIENT_ID"), client)
		if err != nil {
			return err
		}
	}
	perIP, err := getenvNumber("RATE_LIMIT_PER_IP", 600)
	if err != nil {
		return err
	}
	perClient, err := getenvNumber("RATE_LIMIT_PER_CLIENT", 300)
	if err != nil {
		return err
	}
	g.PerIP, g.PerClient = NewRateLimiter(int(perIP), 10000), NewRateLimiter(int(perClient), 10000)

	server := &http.Server{
		Addr:              addr,
		Handler:           g.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second, // a client that stops sending never holds a goroutine
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown) // in-flight requests had their 10 s
	}()
	log.Info("listening", "url", "http://"+addr, "oidc", g.Login != nil, "mcp_resource", g.Resource != nil,
		"dev_login", g.DevLogin, "interceptors", os.Getenv("INTERCEPTORS"))
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// NewPool opens the process's one pool: bounded, with timeouts, every
// connection named. sslmode in the URL is honoured as libpq does.
func NewPool(ctx context.Context, databaseURL string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = ApplicationName
	cfg.ConnConfig.ConnectTimeout = acquireTimeout
	// ADAPT: bounded; the server's max_connections is shared by every process.
	cfg.MaxConns = maxConns
	cfg.MaxConnIdleTime = 30 * time.Second
	return pgxpool.NewWithConfig(ctx, cfg)
}

// buildPipeline: the example interceptors INTERCEPTORS names, in its order.
// Unset, none run and every body passes as bytes.
//
// ADAPT: build your own here, each with its long-lived dependencies (the
// shared http.Client, a signing key from configuration).
func buildPipeline(names []string, embedFailure string, log *slog.Logger) (*Pipeline, error) {
	failure := map[string]Failure{"fail": Fail, "degrade": Degrade}
	embed, ok := failure[embedFailure]
	if !ok {
		return nil, errors.New("EMBED_FAILURE_POLICY must be fail or degrade")
	}
	p := &Pipeline{Log: log}
	for _, name := range names {
		switch name {
		case "embed":
			p.Requests = append(p.Requests, EmbedFields{Embedder: FakeEmbedder{}, Failure: embed, Timeout: 2 * time.Second})
		case "request-id":
			p.Responses = append(p.Responses, RequestIDHeader{})
		default:
			return nil, fmt.Errorf("unknown interceptor %q; known: embed, request-id", name)
		}
	}
	return p, nil
}

func splitList(value, separator string) []string {
	var out []string
	for _, item := range strings.Split(value, separator) {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func getenvNumber(name string, fallback float64) (float64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a number", name, value)
	}
	return number, nil
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
