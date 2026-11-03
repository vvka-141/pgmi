// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project: it is a baseline to learn from,
// not a library. The rules it keeps are in ../PROTOCOL.md ("Extension seams").
//
// The database channel: every call the gateway makes to PostgreSQL for a
// request, behind one interface.
//
//	Channel             one method per call: the policy and the dispatch of
//	                    each protocol the gateway serves
//	Postgres            the real thing, over a pgxpool pool
//	Tracing             a log line per call: op, route, outcome, time
//	SerializationRetry  40001/40P01: the whole transaction again
//	ConnectionRetry     a dropped connection: again, idempotent calls only
//
// A decorator embeds the Channel it wraps and overrides only the calls it
// changes, so cross-cutting behaviour (caching, metrics, a test double) is
// added at one place, main, and no call site changes:
//
//	ConnectionRetry{SerializationRetry{Tracing{Postgres}}}
//
// Both retries sit outside the tracing, so every attempt is a log line of its
// own. The values are built once and only read afterwards: every request
// goroutine shares them, and per-request data travels in the arguments.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RequestContext is what every channel call and every interceptor may read
// about a request. Pass it by value and never write to Annotations: it is
// shared by every interceptor of the request.
//
// There is no principal here: the gateway forwards handles and the database
// resolves who is calling inside the transaction, so nothing before the
// dispatch knows. Handler and Annotations come from the policy call.
type RequestContext struct {
	RequestID   string
	Protocol    string // "rest" or "mcp"
	Method      string // REST: the HTTP method; MCP: the JSON-RPC method
	URL         string // REST: the URL under /api; MCP: /mcp
	Deadline    time.Time
	Handler     string                    // the handler's function name, from the policy
	Annotations map[string]map[string]any // {property: {"x-...": value}}
}

// withPolicy is the context with what the policy says about the handler.
func (rc RequestContext) withPolicy(p Policy) RequestContext {
	rc.Handler, rc.Annotations = p.Handler, p.Annotations
	return rc
}

// Policy is one row of a policy function (api.rest_route_policy,
// api.mcp_request_policy). Isolation "": the connection default.
type Policy struct {
	Isolation   string
	ReadOnly    bool
	Deferrable  bool
	Handler     string
	Annotations map[string]map[string]any
	Modern      bool            // MCP only: the 2026-07-28 era
	Refusal     json.RawMessage // MCP only: the answer instead of a dispatch
}

// RestResult is what api.rest_invoke answered; header names are lower case.
type RestResult struct {
	Status  int
	Headers map[string]string
	Content []byte
}

// MCPHeaders are the three headers api.mcp_request_policy checks against the
// body, as sent; nil when absent. The gateway knows no protocol version.
type MCPHeaders struct {
	ProtocolVersion, Method, Name *string
}

// Channel has one entry per database call. ADAPT: a gateway that serves RPC
// adds RPCPolicy/RPCInvoke here, and every decorator gets them too.
type Channel interface {
	RestPolicy(ctx context.Context, rc RequestContext, requested string) (Policy, error)
	RestInvoke(ctx context.Context, rc RequestContext, p Policy, headers map[string]string, body []byte) (RestResult, error)
	MCPPolicy(ctx context.Context, rc RequestContext, request []byte, requested string, h MCPHeaders) (Policy, error)
	// MCPRequest answers the JSON-RPC envelope; nil for a notification.
	MCPRequest(ctx context.Context, rc RequestContext, p Policy, request []byte, caller json.RawMessage) (json.RawMessage, error)
}

// ErrUnavailable: no pooled connection within the acquire timeout. Answered
// 503 at once; it is not a lost connection, so nothing waits it out twice.
var ErrUnavailable = errors.New("no database connection is free")

// RetriesExhaustedError: every attempt lost a serialization race. Nothing was
// applied and the same call may succeed when repeated.
type RetriesExhaustedError struct {
	SQLState string
	Attempts int
}

func (e *RetriesExhaustedError) Error() string {
	return fmt.Sprintf("serialization failure %s, %d attempts", e.SQLState, e.Attempts)
}

// Postgres runs each policy call as one statement on a pooled connection and
// each dispatch in one transaction opened with the resolved characteristics
// (BEGIN ISOLATION LEVEL ... READ ONLY ...), so a pooled connection carries
// nothing from one request to the next.
//
// The policy costs one round trip per request. It changes only at deploy
// time: a caching decorator would key it on api.catalog_version().
type Postgres struct {
	Pool           *pgxpool.Pool
	AcquireTimeout time.Duration
}

// acquire waits at most AcquireTimeout; an exhausted pool is ErrUnavailable,
// never a request that hangs.
func (p *Postgres) acquire(ctx context.Context) (*pgxpool.Conn, error) {
	waiting, cancel := context.WithTimeout(ctx, p.AcquireTimeout)
	defer cancel()
	conn, err := p.Pool.Acquire(waiting)
	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrUnavailable
	}
	return conn, err
}

var isoLevels = map[string]pgx.TxIsoLevel{
	"read committed": pgx.ReadCommitted, "repeatable read": pgx.RepeatableRead, "serializable": pgx.Serializable,
}

// dispatch runs fn in one transaction opened with the policy. The commit is
// inside: it can fail with 40001 too, and SerializationRetry must see that.
func (p *Postgres) dispatch(ctx context.Context, pol Policy, fn func(pgx.Tx) error) error {
	conn, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	options := pgx.TxOptions{IsoLevel: isoLevels[pol.Isolation], AccessMode: pgx.ReadWrite}
	if pol.ReadOnly {
		options.AccessMode = pgx.ReadOnly
	}
	if pol.Deferrable {
		options.DeferrableMode = pgx.Deferrable
	}
	return pgx.BeginTxFunc(ctx, conn, options, fn)
}

func (p *Postgres) policyRow(ctx context.Context, sql string, args []any, dest ...any) error {
	conn, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	return conn.QueryRow(ctx, sql, args...).Scan(dest...)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (p *Postgres) RestPolicy(ctx context.Context, rc RequestContext, requested string) (Policy, error) {
	var pol Policy
	var isolation, handler *string
	err := p.policyRow(ctx,
		`SELECT transaction_isolation, transaction_read_only, transaction_deferrable, handler_name, input_annotations
		 FROM api.rest_route_policy($1, $2, $3)`,
		[]any{rc.Method, rc.URL, nullable(requested)},
		&isolation, &pol.ReadOnly, &pol.Deferrable, &handler, &pol.Annotations)
	if errors.Is(err, pgx.ErrNoRows) {
		// No such route: open with the defaults and let the invoke answer 404.
		return Policy{}, nil
	}
	if err != nil {
		return Policy{}, fmt.Errorf("rest policy: %w", err)
	}
	pol.Isolation, pol.Handler = deref(isolation), deref(handler)
	return pol, nil
}

func (p *Postgres) RestInvoke(ctx context.Context, rc RequestContext, pol Policy, headers map[string]string, body []byte) (RestResult, error) {
	names := make([]string, 0, len(headers))
	values := make([]string, 0, len(headers))
	for name, value := range headers {
		names = append(names, name)
		values = append(values, value)
	}
	if len(body) == 0 {
		body = nil
	}
	var res RestResult
	var raw map[string]string
	err := p.dispatch(ctx, pol, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT r.status_code, extensions.hstore_to_json(r.headers), r.content
			 FROM api.rest_invoke($1, $2, extensions.hstore($3::text[], $4::text[]), $5::bytea) r`,
			rc.Method, rc.URL, names, values, body).Scan(&res.Status, &raw, &res.Content)
	})
	if err != nil {
		return RestResult{}, fmt.Errorf("rest invoke: %w", err)
	}
	res.Headers = make(map[string]string, len(raw))
	for name, value := range raw {
		res.Headers[strings.ToLower(name)] = value
	}
	return res, nil
}

func (p *Postgres) MCPPolicy(ctx context.Context, rc RequestContext, request []byte, requested string, h MCPHeaders) (Policy, error) {
	var pol Policy
	var isolation, handler *string
	err := p.policyRow(ctx,
		`SELECT transaction_isolation, transaction_read_only, transaction_deferrable, handler_name, input_annotations,
		        modern, refusal
		 FROM api.mcp_request_policy($1::jsonb, $2, $3, $4, $5)`,
		[]any{string(request), nullable(requested), h.ProtocolVersion, h.Method, h.Name},
		&isolation, &pol.ReadOnly, &pol.Deferrable, &handler, &pol.Annotations, &pol.Modern, &pol.Refusal)
	if err != nil {
		return Policy{}, fmt.Errorf("mcp policy: %w", err)
	}
	pol.Isolation, pol.Handler = deref(isolation), deref(handler)
	return pol, nil
}

func (p *Postgres) MCPRequest(ctx context.Context, rc RequestContext, pol Policy, request []byte, caller json.RawMessage) (json.RawMessage, error) {
	var envelope json.RawMessage
	var callerArg *string
	if caller != nil {
		callerArg = new(string(caller))
	}
	err := p.dispatch(ctx, pol, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT (api.mcp_handle_request($1::jsonb, $2::jsonb)).envelope`,
			string(request), callerArg).Scan(&envelope)
	})
	if err != nil {
		return nil, fmt.Errorf("mcp request: %w", err)
	}
	return envelope, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Tracing logs one line per call with the request id, so a slow or failing
// request can be followed from the gateway log into the database's exchange
// log. Never bodies or headers: they carry personal data and session handles.
// ADAPT: OpenTelemetry spans instead of (or beside) log lines.
type Tracing struct {
	Channel
	Log *slog.Logger
}

func (t Tracing) trace(ctx context.Context, rc RequestContext, op string, started time.Time, outcome string, err error) {
	attrs := []any{"request_id", rc.RequestID, "op", op, "method", rc.Method, "url", rc.URL,
		"ms", float64(time.Since(started).Microseconds()) / 1000}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			attrs = append(attrs, "sqlstate", pgErr.Code)
		}
		t.Log.ErrorContext(ctx, "trace", append(attrs, "error", err)...)
		return
	}
	t.Log.InfoContext(ctx, "trace", append(attrs, "outcome", outcome)...)
}

func (t Tracing) RestPolicy(ctx context.Context, rc RequestContext, requested string) (Policy, error) {
	started := time.Now()
	p, err := t.Channel.RestPolicy(ctx, rc, requested)
	t.trace(ctx, rc, "rest.policy", started, "handler="+p.Handler, err)
	return p, err
}

func (t Tracing) RestInvoke(ctx context.Context, rc RequestContext, p Policy, headers map[string]string, body []byte) (RestResult, error) {
	started := time.Now()
	r, err := t.Channel.RestInvoke(ctx, rc, p, headers, body)
	t.trace(ctx, rc, "rest.invoke", started, fmt.Sprintf("status=%d", r.Status), err)
	return r, err
}

func (t Tracing) MCPPolicy(ctx context.Context, rc RequestContext, request []byte, requested string, h MCPHeaders) (Policy, error) {
	started := time.Now()
	p, err := t.Channel.MCPPolicy(ctx, rc, request, requested, h)
	outcome := "handler=" + p.Handler
	if p.Refusal != nil {
		outcome = "refused"
	}
	t.trace(ctx, rc, "mcp.policy", started, outcome, err)
	return p, err
}

func (t Tracing) MCPRequest(ctx context.Context, rc RequestContext, p Policy, request []byte, caller json.RawMessage) (json.RawMessage, error) {
	started := time.Now()
	envelope, err := t.Channel.MCPRequest(ctx, rc, p, request, caller)
	outcome := "result"
	switch {
	case envelope == nil:
		outcome = "notification"
	case strings.Contains(string(envelope), `"error"`):
		outcome = "error"
	}
	t.trace(ctx, rc, "mcp.request", started, outcome, err)
	return envelope, err
}

// backoff is exponential with full jitter: unjittered losers of a race retry
// in lockstep and collide again.
func backoff(attempt int, base time.Duration) time.Duration {
	ceiling := min(time.Second, base<<attempt)
	if ceiling <= 0 {
		return 0
	}
	return rand.N(ceiling)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SerializationRetry: 40001 (serialization failure) and 40P01 (deadlock). The
// database rolled the whole transaction back, so running it again is safe for
// any call, writes included. Each attempt is a new transaction and therefore a
// fresh snapshot; a savepoint or a loop in PL/pgSQL keeps the old snapshot and
// never converges. After the last attempt, *RetriesExhaustedError.
//
// A handler on a route that can hit 40001 must be safe to run twice: a side
// effect outside the transaction (an HTTP call, a queue publish) happens again.
type SerializationRetry struct {
	Channel
	Attempts  int
	BaseDelay time.Duration
}

func serializationFailure(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01") {
		return pgErr.Code, true
	}
	return "", false
}

func retrying[T any](ctx context.Context, s SerializationRetry, call func() (T, error)) (T, error) {
	attempts := max(s.Attempts, 1)
	for attempt := 0; ; attempt++ {
		result, err := call()
		code, retryable := serializationFailure(err)
		if !retryable {
			return result, err
		}
		if attempt == attempts-1 {
			return result, &RetriesExhaustedError{SQLState: code, Attempts: attempts}
		}
		if err := sleep(ctx, backoff(attempt, s.BaseDelay)); err != nil {
			return result, err
		}
	}
}

func (s SerializationRetry) RestInvoke(ctx context.Context, rc RequestContext, p Policy, headers map[string]string, body []byte) (RestResult, error) {
	return retrying(ctx, s, func() (RestResult, error) { return s.Channel.RestInvoke(ctx, rc, p, headers, body) })
}

func (s SerializationRetry) MCPRequest(ctx context.Context, rc RequestContext, p Policy, request []byte, caller json.RawMessage) (json.RawMessage, error) {
	return retrying(ctx, s, func() (json.RawMessage, error) { return s.Channel.MCPRequest(ctx, rc, p, request, caller) })
}

// connectionLost: a failure of the connection, not an answer from the server.
// A broken socket, a failed connect, class 08 (connection exception) or
// 57P01..57P03 (the server shut down). Any other SQLSTATE is an answer, 40001
// included; ErrUnavailable is no connection at all and is answered at once.
func connectionLost(err error) bool {
	if err == nil || errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return strings.HasPrefix(pgErr.Code, "08") || pgErr.Code == "57P01" || pgErr.Code == "57P02" || pgErr.Code == "57P03"
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	return errors.As(err, &connectErr) || errors.As(err, &netErr) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || pgconn.SafeToRetry(err)
}

// ConnectionRetry sends a call again when the connection dropped, ONLY if
// sending it twice cannot apply anything twice: the policy lookups (STABLE
// functions) and a dispatch the database runs READ ONLY (the route declared
// readOnly, so PostgreSQL refuses any write in it). A write is never re-sent:
// the connection can drop after the server committed and before the answer
// arrived, and a second send would apply it twice. MCP is never read-only
// (every exchange is logged), so MCPRequest is not overridden here.
//
// The method name says nothing: a GET handler can write. The database's
// readOnly declaration does.
type ConnectionRetry struct {
	Channel
	Attempts  int
	BaseDelay time.Duration
	Log       *slog.Logger
}

func reconnecting[T any](ctx context.Context, c ConnectionRetry, rc RequestContext, op string, call func() (T, error)) (T, error) {
	attempts := max(c.Attempts, 1)
	for attempt := 0; ; attempt++ {
		result, err := call()
		if !connectionLost(err) || attempt == attempts-1 {
			return result, err
		}
		c.Log.WarnContext(ctx, "connection lost, sending again", "request_id", rc.RequestID, "op", op,
			"attempt", attempt+2, "error", err)
		if err := sleep(ctx, backoff(attempt, c.BaseDelay)); err != nil {
			return result, err
		}
	}
}

func (c ConnectionRetry) RestPolicy(ctx context.Context, rc RequestContext, requested string) (Policy, error) {
	return reconnecting(ctx, c, rc, "rest.policy", func() (Policy, error) { return c.Channel.RestPolicy(ctx, rc, requested) })
}

func (c ConnectionRetry) RestInvoke(ctx context.Context, rc RequestContext, p Policy, headers map[string]string, body []byte) (RestResult, error) {
	if !p.ReadOnly {
		return c.Channel.RestInvoke(ctx, rc, p, headers, body)
	}
	return reconnecting(ctx, c, rc, "rest.invoke", func() (RestResult, error) {
		return c.Channel.RestInvoke(ctx, rc, p, headers, body)
	})
}

func (c ConnectionRetry) MCPPolicy(ctx context.Context, rc RequestContext, request []byte, requested string, h MCPHeaders) (Policy, error) {
	return reconnecting(ctx, c, rc, "mcp.policy", func() (Policy, error) {
		return c.Channel.MCPPolicy(ctx, rc, request, requested, h)
	})
}
