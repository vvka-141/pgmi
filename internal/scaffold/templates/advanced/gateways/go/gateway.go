// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library. Namespaces, modules and package names are
// examples: rename them to match the project and add its specifics freely.
// The wire rules it keeps are in ../PROTOCOL.md.
//
// The HTTP side: routes, the middleware every route shares, REST under /api,
// and the one place where an error becomes a problem answer.
//
//	ANY  /api/...    REST: api.rest_invoke with the path after /api, the
//	                 login session from the session cookie only
//	POST /mcp        MCP, both protocol eras (mcp.go)
//	GET  /auth/login, GET /auth/callback  OIDC sign-in (login.go)
//	POST /auth/logout, POST /auth/dev-login (DEV_LOGIN=1)
//	GET  /.well-known/oauth-protected-resource[/mcp]  with MCP_RESOURCE (resource.go)
//	GET  /health     200 {"status": "healthy"}
//	anything else    404 pgmi.not_found
//
// One request, one transaction: the policy first (its own short call), then
// the interceptors, then the dispatch in a transaction opened with exactly
// the policy's characteristics (channel.go).
package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ADAPT: the name operators see in pg_stat_activity and the server log.
const ApplicationName = "pgmi-gateway-go"

const (
	restPrefix             = "/api"
	maxRequestBody         = 1 << 20
	defaultRequestDeadline = 30 * time.Second
	retryAfter             = "1"
)

// Gateway holds everything a request needs; main builds it once and every
// request goroutine only reads it.
type Gateway struct {
	Channel        Channel         // every database call of a request (channel.go)
	Pipeline       *Pipeline       // interceptors; nil runs none (interceptors.go)
	DB             *Postgres       // sign-in calls, outside the channel
	Sessions       Sessions        // cookies and CSRF (login.go)
	Login          *OIDCLogin      // nil: no OIDC sign-in, /auth/login is 404
	Resource       *ResourceServer // nil: /mcp takes connection handles only
	PerIP          *RateLimiter    // /mcp and /auth, before anything costs
	PerClient      *RateLimiter    // /mcp, per verified OAuth client
	AllowedOrigins map[string]bool
	DevLogin       bool
	Deadline       time.Duration // per request, the database's statement_timeout included; zero: 30 s
	Log            *slog.Logger
}

// handlerFunc is an http.HandlerFunc that returns its error instead of
// answering it, so every error is answered in one place (writeError).
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (g *Gateway) handle(fn handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			g.writeError(w, r, err)
		}
	})
}

// Routes is the gateway's http.Handler: Go 1.22 method-and-path patterns,
// wrapped in the middleware every route shares.
func (g *Gateway) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /mcp", g.withRateLimit(g.withOrigin(http.HandlerFunc(g.mcp))))
	mux.Handle("/mcp", g.handle(func(http.ResponseWriter, *http.Request) error {
		// JSON answers only: no SSE stream to open (GET), no session to end
		// (DELETE), nothing else to do on the MCP endpoint.
		return &problem{status: http.StatusMethodNotAllowed, title: "Method Not Allowed", code: "pgmi.method_not_allowed",
			headers: map[string]string{"Allow": http.MethodPost}}
	}))

	mux.Handle("POST /rpc", g.withOrigin(http.HandlerFunc(g.rpc)))
	mux.Handle("/rpc", g.handle(func(http.ResponseWriter, *http.Request) error {
		return &problem{status: http.StatusMethodNotAllowed, title: "Method Not Allowed", code: "pgmi.method_not_allowed",
			headers: map[string]string{"Allow": http.MethodPost}}
	}))

	mux.Handle("POST /auth/logout", g.withOrigin(g.handle(g.logout)))
	if g.DevLogin {
		mux.Handle("POST /auth/dev-login", g.withOrigin(g.handle(g.devLogin)))
	}
	if g.Login != nil {
		mux.Handle("GET /auth/login", g.withRateLimit(g.handle(g.login)))
		mux.Handle("GET /auth/callback", g.withRateLimit(g.handle(g.callback)))
	}
	if g.Resource != nil {
		for path := range g.Resource.MetadataPaths {
			mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, g.Resource.Metadata())
			})
		}
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	})
	mux.Handle("/", g.handle(func(http.ResponseWriter, *http.Request) error { return notFound() }))
	rest := g.withOrigin(g.handle(g.rest))
	// /api goes around the mux: ServeMux cleans paths and redirects /api//x,
	// but the path under /api is the database's to match, exactly as sent.
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path := r.URL.EscapedPath(); path == restPrefix || strings.HasPrefix(path, restPrefix+"/") {
			rest.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return g.withRequestScope(g.withAccessLog(routes))
}

// -- Middleware -------------------------------------------------------------------

// withRequestScope gives the request its id and its deadline: it is abandoned
// after Deadline. The context carries both to every interceptor, to the
// database, where each transaction sets what is left as its
// statement_timeout, and to the error answer, which names the id.
func (g *Gateway) withRequestScope(next http.Handler) http.Handler {
	deadline := cmp.Or(g.Deadline, defaultRequestDeadline)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), requestIDKey{}, strings.ToLower(rand.Text())), deadline)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

// withAccessLog logs the path only: a callback's query carries the
// authorization code.
func (g *Gateway) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		g.Log.InfoContext(r.Context(), "request", "method", r.Method, "path", r.URL.Path,
			"status", recorder.status, "ms", time.Since(started).Milliseconds())
	})
}

// Standard web plumbing, not pgmi-specific: replace it with your framework's own.
// withOrigin: DNS-rebinding and cross-site protection. A browser sends
// Origin; any value not on the allowlist is refused. Non-browser clients
// send none.
func (g *Gateway) withOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !g.AllowedOrigins[origin] {
			g.writeError(w, r, &problem{status: http.StatusForbidden, title: "Forbidden", code: "pgmi.origin_refused"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withRateLimit counts the client address first, before anything costs: a
// flood of forged tokens stops here and never reaches the identity provider,
// and a flood of /auth/login cannot push real sign-ins out of the store.
func (g *Gateway) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ok, wait := g.PerIP.Allow(host); !ok {
			g.writeError(w, r, rateLimited(wait))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// -- Problems: the one place an error becomes an answer ------------------------------

// problem is an RFC 9457 answer the gateway gives itself. Clients branch on
// code, never on title or detail.
type problem struct {
	status  int
	title   string
	code    string
	detail  string
	headers map[string]string
}

func (p *problem) Error() string { return p.code }

func notFound() *problem {
	return &problem{status: http.StatusNotFound, title: "Not Found", code: "pgmi.not_found"}
}

func rateLimited(wait int) *problem {
	return &problem{status: http.StatusTooManyRequests, title: "Too Many Requests", code: "pgmi.rate_limited",
		headers: map[string]string{"Retry-After": strconv.Itoa(wait)}}
}

// writeError maps every error a REST or sign-in handler returns to its
// answer. Anything unknown is logged and answered 500 without its text: the
// client never sees SQL.
func (g *Gateway) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var p *problem
	var exhausted *RetriesExhaustedError
	var failed *InterceptorFailedError
	switch {
	case errors.As(err, &p):
	case errors.As(err, &exhausted):
		p = &problem{status: http.StatusConflict, title: "Conflict", code: "pgmi.transaction_retryable",
			detail: "the request conflicted with a concurrent change; repeat it", headers: map[string]string{"Retry-After": retryAfter}}
	case errors.Is(err, ErrUnavailable):
		g.Log.WarnContext(r.Context(), "no pooled connection", "path", r.URL.Path)
		p = &problem{status: http.StatusServiceUnavailable, title: "Service Unavailable", code: "pgmi.unavailable",
			detail: "no database connection is free; repeat the request", headers: map[string]string{"Retry-After": retryAfter}}
	case errors.As(err, &failed):
		// After the commit the request was applied: say so, so the client
		// does not repeat a write that already happened.
		detail := "the request was not applied"
		if failed.Applied {
			detail = "the request was applied; its answer could not be completed"
		}
		p = &problem{status: http.StatusBadGateway, title: "Bad Gateway", code: "pgmi.interceptor_failed", detail: detail,
			headers: map[string]string{"X-Request-Id": requestID(r)}}
	case deadlineExceeded(r, err):
		g.Log.WarnContext(r.Context(), "deadline exceeded", "request_id", requestID(r), "path", r.URL.Path, "error", err)
		p = &problem{status: http.StatusGatewayTimeout, title: "Gateway Timeout", code: "pgmi.deadline_exceeded",
			detail: "the request ran past its deadline and was cancelled", headers: map[string]string{"X-Request-Id": requestID(r)}}
	default:
		g.Log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		p = &problem{status: http.StatusInternalServerError, title: "Internal Server Error", code: "pgmi.internal_error"}
	}
	body := map[string]any{"type": "about:blank", "title": p.title, "status": p.status, "code": p.code}
	if p.detail != "" {
		body["detail"] = p.detail
	}
	// A problem replaces whatever headers were already set for the answer,
	// except the cookies a sign-in set or cleared on purpose.
	cookies := w.Header().Values("Set-Cookie")
	clear(w.Header())
	for _, c := range cookies {
		w.Header().Add("Set-Cookie", c)
	}
	for name, value := range p.headers {
		w.Header().Set(name, value)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeBody(w, p.status, "application/problem+json", body)
}

// deadlineExceeded: the request's own deadline ended it, in the database
// (ErrDeadlineExceeded) or anywhere else that watched its context. A timeout
// of a call's own (an identity provider's, say) is not the request's.
func deadlineExceeded(r *http.Request, err error) bool {
	return errors.Is(err, ErrDeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) && errors.Is(r.Context().Err(), context.DeadlineExceeded)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	writeBody(w, status, "application/json", v)
}

func writeBody(w http.ResponseWriter, status int, contentType string, v any) {
	raw, _ := json.Marshal(v) // maps and slices of plain values always marshal
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(status)
	_, _ = w.Write(raw) // the client hung up: nothing left to tell it
}

// -- Requests ----------------------------------------------------------------------

type requestIDKey struct{}

// newRequestContext starts a request's RequestContext from the id and the
// deadline withRequestScope gave it.
func newRequestContext(r *http.Request, protocol, method, url string) RequestContext {
	deadline, _ := r.Context().Deadline()
	return RequestContext{RequestID: requestID(r), Protocol: protocol, Method: method, URL: url, Deadline: deadline}
}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

// readBody reads at most maxRequestBody; more is 413 before the database is
// called. A Content-Length that is not a number never gets here: net/http
// answers it 400 itself.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	tooLarge := &problem{status: http.StatusRequestEntityTooLarge, title: "Content Too Large", code: "pgmi.body_too_large",
		detail: fmt.Sprintf("the limit is %d bytes", maxRequestBody)}
	if r.ContentLength > maxRequestBody {
		return nil, tooLarge
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if tooBig := (*http.MaxBytesError)(nil); errors.As(err, &tooBig) {
		return nil, tooLarge
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return nil, &problem{status: http.StatusRequestTimeout, title: "Request Timeout", code: "pgmi.request_timeout",
			detail: "the request body did not arrive in time", headers: map[string]string{"Connection": "close"}}
	}
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

var (
	// pgmi-specific: what may reach the database; see ../PROTOCOL.md "Identity".
	// Session and connection handles the gateway did not issue, the cookie
	// that carries the one it did, and hop-by-hop headers.
	notForwarded = map[string]bool{
		"x-login-session": true, "x-connection": true, "cookie": true, "x-csrf-token": true,
		"connection": true, "keep-alive": true, "proxy-connection": true, "te": true, "trailer": true,
		"transfer-encoding": true, "upgrade": true,
	}
	singleValued = map[string]bool{
		"authorization": true, "content-length": true, "content-type": true, "host": true, "origin": true,
		"x-csrf-token": true, "x-organization-session": true, "x-pgmi-transaction-isolation": true,
	}
	notCopied = map[string]bool{"content-length": true, "transfer-encoding": true, "connection": true}
)

func byLowerName(in http.Header) map[string][]string {
	out := make(map[string][]string, len(in))
	for name, values := range in {
		key := strings.ToLower(name)
		out[key] = append(out[key], values...)
	}
	return out
}

// HeaderCollision names a header that must come once but came more often,
// in any letter case; "" when there is none. One of two values would be
// guessed at.
func HeaderCollision(in http.Header) string {
	for name, values := range byLowerName(in) {
		if len(values) > 1 && singleValued[name] {
			return name
		}
	}
	return ""
}

// IncomingHeaders is every header the client sent: lower-cased names, each
// once, repeated values joined (RFC 9110 §5.3; a cookie with "; ", RFC 6265
// §5.4). Interceptors see this, credentials included.
func IncomingHeaders(in http.Header) map[string]string {
	out := make(map[string]string, len(in)+1)
	for name, values := range byLowerName(in) {
		separator := ", "
		if name == "cookie" {
			separator = "; "
		}
		out[name] = strings.Join(values, separator)
	}
	return out
}

// ForwardedHeaders is what the database sees: the incoming headers without
// the handles and cookie the client sent, the login session from the
// gateway's cookie only.
func ForwardedHeaders(in http.Header, loginSession string) map[string]string {
	return withSession(IncomingHeaders(in), loginSession)
}

// withSession drops every handle the client sent and sets the gateway's own.
// It runs again after the interceptors: identity is the gateway's, whatever
// an interceptor did to the headers.
func withSession(headers map[string]string, loginSession string) map[string]string {
	out := make(map[string]string, len(headers)+1)
	for name, value := range headers {
		if name = strings.ToLower(name); !notForwarded[name] {
			out[name] = value
		}
	}
	if loginSession != "" {
		out["x-login-session"] = loginSession
	}
	return out
}

var (
	separators = regexp.MustCompile(`[\s_-]+`)
	// PostgreSQL folds "read uncommitted" onto "read committed"; so do we.
	isolationNames = map[string]string{"read committed": "read committed", "read uncommitted": "read committed",
		"repeatable read": "repeatable read", "serializable": "serializable"}
)

// NormalizeIsolation reads X-PGMI-Transaction-Isolation: case and separators
// free; false when the level is unknown. It is escalation, not obligation: the
// database resolves max(route floor, requested).
func NormalizeIsolation(value string) (string, bool) {
	level, ok := isolationNames[separators.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), " ")]
	return level, ok
}

// -- REST ----------------------------------------------------------------------------

// restURL is the request target under /api, query included, as the client
// sent it: not cleaned, not decoded. The database routes it.
func restURL(r *http.Request) string {
	target := r.RequestURI
	if !strings.HasPrefix(target, restPrefix) {
		target = r.URL.RequestURI() // an absolute-form target, sent to a proxy
	}
	url := strings.TrimPrefix(target, restPrefix)
	if url == "" || url[0] == '?' {
		url = "/" + url
	}
	return url
}

// rest serves /api/... over api.rest_invoke, as the login transport contract
// says: the login session from the cookie, never from a client header.
func (g *Gateway) rest(w http.ResponseWriter, r *http.Request) error {
	if name := HeaderCollision(r.Header); name != "" {
		return &problem{status: http.StatusBadRequest, title: "Bad Request", code: "pgmi.header_collision",
			detail: "header " + name + " is sent more than once"}
	}
	loginSession := cookieValue(r, sessionCookie)
	if !g.Sessions.CSRFOK(r.Method, loginSession, r.Header.Get("X-CSRF-Token")) {
		return csrfRefused()
	}
	headers := ForwardedHeaders(r.Header, loginSession)
	requested, err := requestedIsolation(headers)
	if err != nil {
		return err
	}
	body, err := readBody(w, r)
	if err != nil {
		return err
	}

	rc := newRequestContext(r, "rest", r.Method, restURL(r))
	ctx := r.Context()
	// pgmi-specific: the database decides, before the transaction opens; see
	// ../PROTOCOL.md "One request, one transaction".
	pol, err := g.Channel.RestPolicy(ctx, rc, requested)
	if err != nil {
		return err
	}
	rc = rc.withPolicy(pol)
	if selected := g.Pipeline.RequestsFor(rc); len(selected) > 0 {
		// The policy call returned its connection: none is held while an
		// interceptor waits on another service.
		req := &Request{Method: rc.Method, URL: rc.URL, Message: Message{Header: IncomingHeaders(r.Header), Body: body}}
		if err := g.Pipeline.RunRequests(ctx, rc, selected, req); err != nil {
			return err
		}
		headers, body = withSession(req.Header, loginSession), req.Body
		if req.Method != rc.Method || req.URL != rc.URL {
			// Another route is another handler: resolve its policy.
			rc.Method, rc.URL = req.Method, req.URL
			if pol, err = g.Channel.RestPolicy(ctx, rc, requested); err != nil {
				return err
			}
			rc = rc.withPolicy(pol)
		}
	}
	result, err := g.Channel.RestInvoke(ctx, rc, pol, headers, body)
	if err != nil {
		return err
	}
	if selected := g.Pipeline.ResponsesFor(rc); len(selected) > 0 {
		resp := &Response{Status: result.Status, Message: Message{Header: result.Headers, Body: result.Content}}
		if err := g.Pipeline.RunResponses(ctx, rc, selected, resp); err != nil {
			return err
		}
		result = RestResult{Status: resp.Status, Headers: resp.Header, Content: resp.Body}
	}

	writeResult(w, r, result)
	return nil
}

// requestedIsolation is the X-PGMI-Transaction-Isolation the client asked for,
// "" for none.
func requestedIsolation(headers map[string]string) (string, error) {
	value, ok := headers["x-pgmi-transaction-isolation"]
	if !ok {
		return "", nil
	}
	level, known := NormalizeIsolation(value)
	if !known {
		return "", &problem{status: http.StatusBadRequest, title: "Bad Request", code: "pgmi.transaction_isolation_unsupported"}
	}
	return level, nil
}

// writeResult answers what the database answered. It copies every header the
// database returned (Vary, WWW-Authenticate, Location, ...) except hop-by-hop
// ones. HEAD carries the length GET would have sent (RFC 9110 §8.6) and no
// content.
func writeResult(w http.ResponseWriter, r *http.Request, result RestResult) {
	for name, value := range result.Headers {
		if !notCopied[name] {
			w.Header().Set(name, value)
		}
	}
	if r.Method == http.MethodHead {
		if length, ok := result.Headers["content-length"]; ok {
			w.Header().Set("Content-Length", length)
		}
		w.WriteHeader(result.Status)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(result.Content)))
	w.WriteHeader(result.Status)
	_, _ = w.Write(result.Content) // the client hung up: nothing left to tell it
}
