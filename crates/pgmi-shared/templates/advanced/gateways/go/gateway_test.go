// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project. Namespaces, modules and
// package names are examples: rename them to match the project and add its
// specifics freely.
//
// The unit tests run anywhere. The database tests need an env=dev deploy and
// DATABASE_URL as a login in the api role; without it, or with -short, they
// are skipped. The other *_test.go files test the seams, sign-in and the
// resource server without a database.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ADAPT: as in gateway.go.
const wantApplicationName = "pgmi-gateway-go"

var quiet = slog.New(slog.DiscardHandler)

func TestAVerifyingSSLModeNeedsATrustAnchor(t *testing.T) {
	t.Setenv("PGSSLMODE", "")
	t.Setenv("PGSSLROOTCERT", "")
	const base = "postgresql://gw:pw@db.example.com/app"
	for url, want := range map[string]string{
		base + "?sslmode=verify-ca":                             "sslmode=verify-ca needs sslrootcert in DATABASE_URL: the CA file to trust, or system",
		base + "?sslmode=verify-full":                           "sslmode=verify-full needs sslrootcert in DATABASE_URL: the CA file to trust, or system",
		base + "?sslmode=verify-ca&sslrootcert=system":          "sslrootcert=system needs sslmode=verify-full, not verify-ca",
		"host=db.example.com sslmode=verify-full":               "sslmode=verify-full needs sslrootcert in DATABASE_URL: the CA file to trust, or system",
		base + "?sslmode=verify-full&sslrootcert=system":        "",
		base + "?sslmode=verify-ca&sslrootcert=%2Fetc%2Fca.pem": "",
		base + "?sslmode=require":                               "",
		base:                                                    "",
	} {
		err := checkTLSTrust(url)
		if got := fmt.Sprint(err); (want == "" && err != nil) || (want != "" && got != want) {
			t.Errorf("%s: got %v, want %q", url, err, want)
		}
	}

	if _, err := NewPool(t.Context(), base+"?sslmode=verify-ca", 1); err == nil || !strings.Contains(err.Error(), "needs sslrootcert") {
		t.Errorf("NewPool must refuse verify-ca without sslrootcert before connecting, got %v", err)
	}

	t.Setenv("PGSSLMODE", "verify-full")
	if err := checkTLSTrust(base); err == nil {
		t.Error("PGSSLMODE=verify-full without a root certificate must be refused")
	}
	t.Setenv("PGSSLROOTCERT", "/etc/ca.pem")
	if err := checkTLSTrust(base); err != nil {
		t.Errorf("PGSSLROOTCERT is a trust anchor: %v", err)
	}
}

func TestForwardedHeadersTakeTheSessionFromTheCookieOnly(t *testing.T) {
	in := http.Header{}
	in.Set("X-Login-Session", "ls_forged")
	in.Set("X-Connection", "cn_x")
	in.Set("Accept", "application/json")
	out := ForwardedHeaders(in, "ls_real")
	if out["x-login-session"] != "ls_real" || out["accept"] != "application/json" {
		t.Errorf("forwarded %v", out)
	}
	if _, ok := out["x-connection"]; ok {
		t.Errorf("x-connection must not reach the database: %v", out)
	}
}

func TestAnInterceptorCannotSetTheSession(t *testing.T) {
	written := func() map[string]string {
		return map[string]string{"X-Login-Session": "ls_forged", "X-Connection": "cn_x", "X-Trace": "t"}
	}
	if out := withSession(written(), ""); !maps.Equal(out, map[string]string{"x-trace": "t"}) {
		t.Errorf("without a cookie, forwarded %v", out)
	}
	if out := withSession(written(), "ls_real"); !maps.Equal(out, map[string]string{"x-trace": "t", "x-login-session": "ls_real"}) {
		t.Errorf("with a cookie, forwarded %v", out)
	}
}

func TestForwardedHeadersJoinARepeatedName(t *testing.T) {
	in := http.Header{"Accept": {"application/json"}, "accept": {"*/*"}}
	out := ForwardedHeaders(in, "")
	if out["accept"] != "application/json, */*" && out["accept"] != "*/*, application/json" {
		t.Errorf("accept forwarded as %q", out["accept"])
	}
	if name := HeaderCollision(in); name != "" {
		t.Errorf("a list header must not collide: %s", name)
	}
}

func TestIncomingHeadersJoinARepeatedCookieWithSemicolons(t *testing.T) {
	out := IncomingHeaders(http.Header{"Cookie": {"a=1", "b=2"}})
	if out["cookie"] != "a=1; b=2" {
		t.Errorf("cookie joined as %q", out["cookie"])
	}
}

func TestHeaderCollisionRefusesASingleValuedName(t *testing.T) {
	for _, name := range []string{"Authorization", "Content-Type", "Origin", "X-CSRF-Token", "X-Organization-Session", "X-PGMI-Transaction-Isolation"} {
		in := http.Header{name: {"a"}, strings.ToLower(name): {"b"}}
		if got := HeaderCollision(in); got != strings.ToLower(name) {
			t.Errorf("HeaderCollision(%s twice) = %q", name, got)
		}
	}
}

func serve(g *Gateway, r *http.Request) *http.Response {
	if g.Log == nil {
		g.Log = quiet
	}
	w := httptest.NewRecorder()
	g.Routes().ServeHTTP(w, r)
	return w.Result()
}

func problemCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct{ Code string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("status %d, not a problem: %v", resp.StatusCode, err)
	}
	return body.Code
}

func TestARepeatedSingleValuedHeaderIs400(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	r.Header.Add("X-Organization-Session", "a")
	r.Header.Add("X-Organization-Session", "b")
	resp := serve(&Gateway{}, r)
	if code := problemCode(t, resp); resp.StatusCode != http.StatusBadRequest || code != "pgmi.header_collision" {
		t.Errorf("got %d %s", resp.StatusCode, code)
	}
}

func TestABodyOverTheLimitIs413BeforeTheDatabase(t *testing.T) {
	big := bytes.Repeat([]byte("a"), maxRequestBody+1)
	declared := httptest.NewRequest(http.MethodPost, "/api/echo", bytes.NewReader(big))
	chunked := httptest.NewRequest(http.MethodPost, "/api/echo", io.MultiReader(bytes.NewReader(big)))
	chunked.ContentLength = -1
	for name, r := range map[string]*http.Request{"declared": declared, "chunked": chunked} {
		resp := serve(&Gateway{}, r)
		if code := problemCode(t, resp); resp.StatusCode != http.StatusRequestEntityTooLarge || code != "pgmi.body_too_large" {
			t.Errorf("%s: got %d %s", name, resp.StatusCode, code)
		}
	}
}

func TestANonDecimalContentLengthIs400(t *testing.T) {
	server := httptest.NewServer((&Gateway{Log: quiet}).Routes())
	defer server.Close()
	for _, length := range []string{"-1", "abc", "1e3"} {
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(conn, "POST /api/echo HTTP/1.1\r\nHost: x\r\nContent-Length: "+length+"\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("Content-Length %s: %v", length, err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Content-Length %s: got %d", length, resp.StatusCode)
		}
		conn.Close()
	}
}

func TestTheRESTPathIsTheDatabases(t *testing.T) {
	for target, want := range map[string]string{
		"/api": "/", "/api?a=1": "/?a=1", "/api/hello?name=a%20b": "/hello?name=a%20b",
		"/api//hello": "//hello", "/api/./hello": "/./hello", "/api/x/../hello": "/x/../hello", "/api/%2e%2e/hello": "/%2e%2e/hello",
	} {
		if got := restURL(httptest.NewRequest(http.MethodGet, target, nil)); got != want {
			t.Errorf("restURL(%s) = %s, want %s", target, got, want)
		}
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Header.Set("Origin", "https://evil.example") // refused by the REST handler, so no database is needed
		if resp := serve(&Gateway{}, r); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s did not reach the REST handler: %d %s", target, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}

func TestRoutes(t *testing.T) {
	g := &Gateway{}
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/health", 200},
		{"GET", "/nowhere", 404},
		{"POST", "/health", 404},
		{"GET", "/apix", 404},
		{"GET", "/auth/login", 404},                           // no OIDC configured
		{"POST", "/auth/dev-login", 404},                      // DEV_LOGIN unset
		{"GET", "/.well-known/oauth-protected-resource", 404}, // no MCP_RESOURCE
		{"GET", "/mcp", 405},
		{"DELETE", "/mcp", 405},
		{"GET", "/rpc", 405},
	} {
		resp := serve(g, httptest.NewRequest(c.method, c.path, nil))
		if resp.StatusCode != c.status {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, resp.StatusCode, c.status)
		}
		if c.status == 405 && (resp.Header.Get("Allow") != "POST" || problemCode(t, resp) != "pgmi.method_not_allowed") {
			t.Errorf("%s %s: Allow %q", c.method, c.path, resp.Header.Get("Allow"))
		}
	}
}

func TestMCPTransportRulesBeforeTheDatabase(t *testing.T) {
	g := &Gateway{AllowedOrigins: map[string]bool{"https://app.example": true}}
	post := func(body, contentType string, headers map[string]string) *http.Response {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return serve(g, r)
	}
	for _, c := range []struct {
		contentType string
		headers     map[string]string
		status      int
		code        string
	}{
		{"application/json", map[string]string{"Origin": "https://evil.example"}, 403, "pgmi.origin_refused"},
		{"text/plain", nil, 415, "pgmi.unsupported_media_type"},
		{"application/json", map[string]string{"Accept": "text/event-stream, application/json;q=0"}, 406, "pgmi.not_acceptable"},
	} {
		if resp := post("{}", c.contentType, c.headers); resp.StatusCode != c.status || problemCode(t, resp) != c.code {
			t.Errorf("%s %v: %d, want %d %s", c.contentType, c.headers, resp.StatusCode, c.status, c.code)
		}
	}
	for _, body := range []string{"not-json", "\xff\xfe{}", "", "{} {}"} {
		resp := post(body, "application/json; charset=utf-8", map[string]string{"MCP-Protocol-Version": "2026-07-28"})
		var envelope struct {
			ID    any
			Error struct{ Code int }
		}
		_ = json.NewDecoder(resp.Body).Decode(&envelope)
		if resp.StatusCode != 400 || envelope.Error.Code != -32700 || envelope.ID != nil {
			t.Errorf("%q: %d %+v", body, resp.StatusCode, envelope)
		}
		if resp.Header.Get("MCP-Protocol-Version") != "2026-07-28" {
			t.Errorf("%q: MCP-Protocol-Version not echoed", body)
		}
	}
	resp := post(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`, "application/json", map[string]string{"X-PGMI-Transaction-Isolation": "snapshot"})
	var envelope struct {
		ID    int
		Error struct {
			Code int
			Data struct{ Code string }
		}
	}
	_ = json.NewDecoder(resp.Body).Decode(&envelope)
	if resp.StatusCode != 400 || envelope.ID != 4 || envelope.Error.Data.Code != "pgmi.transaction_isolation_unsupported" {
		t.Errorf("unknown isolation: %d %+v", resp.StatusCode, envelope)
	}
}

func TestMCPStatus(t *testing.T) {
	for _, c := range []struct {
		envelope string
		modern   bool
		want     int
	}{
		{`{"result":{}}`, true, 200},
		{`{"error":{"code":-32601}}`, true, 404},
		{`{"error":{"code":-32022}}`, true, 400},
		{`{"error":{"code":-32602}}`, true, 200},
		{`{"error":{"code":-32601}}`, false, 200},
		{`{"error":{"code":-32022}}`, false, 200},
	} {
		if got := mcpStatus([]byte(c.envelope), c.modern); got != c.want {
			t.Errorf("mcpStatus(%s, %v) = %d, want %d", c.envelope, c.modern, got, c.want)
		}
	}
}

func TestConnectionHandles(t *testing.T) {
	for authorization, want := range map[string]string{
		"Bearer cn_abc": "cn_abc", "bearer  cn_abc ": "cn_abc", "Bearer eyJhbGciOi": "", "": "", "Basic cn_abc": "",
	} {
		if got := bearerHandle(authorization); got != want {
			t.Errorf("bearerHandle(%q) = %q, want %q", authorization, got, want)
		}
	}
	for envelope, want := range map[string]bool{
		`{"error":{"code":-31001,"data":{"code":"pgmi.connection_unknown"}}}`:      true,
		`{"error":{"code":-31001,"data":{"code":"membership.connection.paused"}}}`: false,
		`{"result":{}}`: false,
	} {
		if got := handleUnknown([]byte(envelope)); got != want {
			t.Errorf("handleUnknown(%s) = %v, want %v", envelope, got, want)
		}
	}
}

func TestAcceptsJSON(t *testing.T) {
	for accept, want := range map[string]bool{
		"application/json": true, "*/*": true, "application/*;q=0.1": true, "text/event-stream, application/json": true,
		"application/json;q=0": false, "text/html": false, "application/json;q=0.0, */*;q=0": false,
		"application/json;q=0, */*": false, "application/*;q=0, application/json": true,
	} {
		if got := acceptsJSON(accept); got != want {
			t.Errorf("acceptsJSON(%q) = %v", accept, got)
		}
	}
}

func TestLogout(t *testing.T) {
	g := &Gateway{Sessions: Sessions{CSRFSecret: []byte("secret")}, AllowedOrigins: map[string]bool{"https://app.example": true}}

	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "ls_a"})
	if resp := serve(g, r); resp.StatusCode != http.StatusForbidden || problemCode(t, resp) != "pgmi.csrf_refused" {
		t.Errorf("without X-CSRF-Token: %d", resp.StatusCode)
	}

	r = httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.Header.Set("Origin", "https://evil.example")
	if resp := serve(g, r); resp.StatusCode != http.StatusForbidden || problemCode(t, resp) != "pgmi.origin_refused" {
		t.Errorf("foreign origin: %d", resp.StatusCode)
	}

	r = httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.Header.Set("Origin", "https://app.example")
	resp := serve(g, r)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("without a session: %d %v", resp.StatusCode, resp.Header)
	}
	want := map[string]string{
		"__Host-session=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Strict": "",
		"__Host-csrf=; Path=/; Max-Age=0; Secure; SameSite=Strict":              "",
	}
	for _, c := range resp.Header.Values("Set-Cookie") {
		delete(want, c)
	}
	if len(want) != 0 {
		t.Errorf("cookies not cleared %v, got %v", want, resp.Header.Values("Set-Cookie"))
	}
}

func TestNormalizeIsolation(t *testing.T) {
	for in, want := range map[string]string{"Repeatable_Read": "repeatable read", "read-uncommitted": "read committed", "SERIALIZABLE": "serializable"} {
		if got, ok := NormalizeIsolation(in); !ok || got != want {
			t.Errorf("NormalizeIsolation(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := NormalizeIsolation("snapshot"); ok {
		t.Error("an unknown level must be refused")
	}
}

func TestCSRF(t *testing.T) {
	s := Sessions{CSRFSecret: []byte("secret")}
	cases := []struct {
		method, session, token string
		want                   bool
	}{
		{"GET", "ls_a", "", true},
		{"POST", "", "", true},
		{"POST", "ls_a", "", false},
		{"POST", "ls_a", s.CSRFToken("ls_b"), false},
		{"POST", "ls_a", s.CSRFToken("ls_a"), true},
	}
	for _, c := range cases {
		if got := s.CSRFOK(c.method, c.session, c.token); got != c.want {
			t.Errorf("CSRFOK(%s, %q, %q) = %v", c.method, c.session, c.token, got)
		}
	}
}

// -- Against the deployed template ------------------------------------------------------

type client struct {
	t       *testing.T
	base    string
	session string
	csrf    string
}

type reply struct {
	status  int
	headers http.Header
	body    map[string]any
	raw     []byte
}

func (c *client) call(method, path string, body any, headers map[string]string, signedIn bool) reply {
	c.t.Helper()
	var payload io.Reader = http.NoBody
	if body != nil {
		raw, _ := json.Marshal(body)
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, payload)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if signedIn {
		req.AddCookie(&http.Cookie{Name: "__Host-session", Value: c.session})
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return reply{resp.StatusCode, resp.Header, decoded, raw}
}

func (c *client) post(path string, body any, headers map[string]string) reply {
	all := map[string]string{"X-CSRF-Token": c.csrf}
	for k, v := range headers {
		all[k] = v
	}
	return c.call(http.MethodPost, path, body, all, true)
}

func assertProblem(t *testing.T, r reply, status int, code string) {
	t.Helper()
	if r.status != status || r.body["code"] != code || !strings.HasPrefix(r.headers.Get("Content-Type"), "application/problem+json") {
		t.Errorf("want %d %s, got %d %v", status, code, r.status, r.body)
	}
}

func databaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if testing.Short() || url == "" {
		t.Skip("needs DATABASE_URL and no -short")
	}
	return url
}

// deployed is the gateway as main composes it, against DATABASE_URL.
func deployed(t *testing.T, poolSize int32, pipeline *Pipeline) (*Gateway, *pgxpool.Pool) {
	t.Helper()
	pool, err := NewPool(t.Context(), databaseURL(t), poolSize)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db := &Postgres{Pool: pool, AcquireTimeout: acquireTimeout}
	channel := ConnectionRetry{Attempts: 2, Log: quiet,
		Channel: SerializationRetry{Attempts: 3, BaseDelay: 10 * time.Millisecond, Channel: Tracing{Log: quiet, Channel: db}}}
	return &Gateway{Channel: channel, Pipeline: pipeline, DB: db, Sessions: Sessions{CSRFSecret: []byte(rand.Text())},
		DevLogin: true, Log: quiet}, pool
}

func TestAgainstTheDeployedTemplate(t *testing.T) {
	g, pool := deployed(t, 10, nil)
	server := httptest.NewServer(g.Routes())
	defer server.Close()
	c := &client{t: t, base: server.URL}

	// Development sign-in. In production the gateway runs OIDC and calls
	// api.open_login_session instead.
	signIn := c.call(http.MethodPost, "/auth/dev-login",
		map[string]string{"subject": "client-go", "email": "client-go@example.com", "displayName": "Go client"}, nil, false)
	if signIn.status != http.StatusNoContent {
		t.Fatalf("dev sign-in: %d %v", signIn.status, signIn.body)
	}
	for _, cookie := range (&http.Response{Header: signIn.headers}).Cookies() {
		switch cookie.Name {
		case "__Host-session":
			c.session = cookie.Value
		case "__Host-csrf":
			c.csrf = cookie.Value
		}
	}

	t.Run("openapi.json lists what the caller may call", func(t *testing.T) {
		anonymous := c.call(http.MethodGet, "/api/openapi.json", nil, nil, false)
		paths, _ := anonymous.body["paths"].(map[string]any)
		if _, ok := paths["/hello"]; !ok || paths["/me"] != nil {
			t.Errorf("anonymous paths: %v", paths)
		}
		signedIn := c.call(http.MethodGet, "/api/openapi.json", nil, nil, true)
		if paths, _ := signedIn.body["paths"].(map[string]any); paths["/me"] == nil {
			t.Errorf("signed-in paths lack /me: %v", paths)
		}
	})

	t.Run("a public route", func(t *testing.T) {
		r := c.call(http.MethodGet, "/api/hello?name=Go", nil, nil, false)
		if r.status != http.StatusOK || r.body["message"] != "Hello, Go!" {
			t.Errorf("got %d %v", r.status, r.body)
		}
	})

	t.Run("an authenticated route", func(t *testing.T) {
		if r := c.call(http.MethodGet, "/api/me", nil, nil, false); r.status != http.StatusUnauthorized {
			t.Errorf("anonymous: %d", r.status)
		}
		if r := c.call(http.MethodGet, "/api/me", nil, nil, true); r.status != http.StatusOK {
			t.Errorf("signed in: %d %v", r.status, r.body)
		}
	})

	t.Run("a client-sent session header is dropped", func(t *testing.T) {
		if r := c.call(http.MethodGet, "/api/me", nil, map[string]string{"X-Login-Session": c.session}, false); r.status != http.StatusUnauthorized {
			t.Errorf("got %d", r.status)
		}
	})

	var organizationSession string
	var organizationID any
	t.Run("coded problems", func(t *testing.T) {
		assertProblem(t, c.call(http.MethodPost, "/api/connections", map[string]any{"clientName": "x"}, nil, true), 403, "pgmi.csrf_refused")
		assertProblem(t, c.post("/api/connections", map[string]any{"clientName": 5}, nil), 400, "pgmi.invalid_body")
		assertProblem(t, c.post("/api/connections", map[string]any{"clientName": "x", "organizationId": "00000000-0000-4000-8000-000000000000", "roleId": "agent.reader"}, nil),
			422, "membership.connection.not_member")

		created := c.post("/api/organizations", map[string]any{"name": "Go client", "slug": "go-" + strings.ToLower(rand.Text()[:8])}, nil)
		if created.status != http.StatusCreated {
			t.Fatalf("create organization: %d %v", created.status, created.body)
		}
		organizationID = created.body["id"]
		opened := c.post("/api/organization-sessions", map[string]any{"organizationId": organizationID}, nil)
		if opened.status != http.StatusOK {
			t.Fatalf("open organization session: %d %v", opened.status, opened.body)
		}
		organizationSession, _ = opened.body["organizationSession"].(string)
		assertProblem(t, c.post("/api/connections", map[string]any{"clientName": "x", "validDays": 999,
			"organizationId": organizationID, "roleId": "agent.reader"},
			map[string]string{"x-organization-session": organizationSession}), 400, "pgmi.invalid_body")
	})

	t.Run("a JSON-RPC action at /rpc", func(t *testing.T) {
		org := map[string]string{"x-organization-session": organizationSession}
		call := func(method string, params map[string]any) map[string]any {
			return map[string]any{"jsonrpc": "2.0", "id": 9, "method": method, "params": params}
		}
		r := c.post("/rpc", call("organization.transfer_ownership",
			map[string]any{"newOwnerId": "00000000-0000-4000-8000-000000000000"}), org)
		rpcErr, _ := r.body["error"].(map[string]any)
		data, _ := rpcErr["data"].(map[string]any)
		if r.status != http.StatusUnprocessableEntity || rpcErr["code"] != float64(-32011) || r.body["id"] != float64(9) ||
			fmt.Sprint(data["invalid-params"]) != "[map[name:newOwnerId reason:not_found]]" {
			t.Errorf("a target outside the organization: %d %v", r.status, r.body)
		}
		notification := map[string]any{"jsonrpc": "2.0", "method": "organization.transfer_ownership",
			"params": map[string]any{"newOwnerId": "00000000-0000-4000-8000-000000000000"}}
		if r := c.post("/rpc", notification, org); r.status != http.StatusAccepted || len(r.raw) != 0 {
			t.Errorf("a notification: %d %q", r.status, r.raw)
		}
		if r := c.post("/rpc", call("no.such.method", nil), org); r.status != http.StatusNotFound {
			t.Errorf("an unknown method: %d %v", r.status, r.body)
		}
		assertProblem(t, c.call(http.MethodPost, "/rpc", call("organization.transfer_ownership", nil), nil, true),
			403, "pgmi.csrf_refused")
	})

	t.Run("MCP through an approved connection", func(t *testing.T) {
		if organizationSession == "" {
			t.Skip("no organization session")
		}
		approved := c.post("/api/connections", map[string]any{"clientName": "Go agent", "organizationId": organizationID,
			"roleId": "agent.reader"}, map[string]string{"x-organization-session": organizationSession})
		handle, _ := approved.body["connection"].(string)
		if approved.status != http.StatusCreated || handle == "" {
			t.Fatalf("approve a connection: %d %v", approved.status, approved.body)
		}
		// ADAPT: echo_note is the example tool in api/examples.sql; call one of yours.
		call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo_note","arguments":{"note":"hi"},` +
			`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(call))
		for k, v := range map[string]string{"Content-Type": "application/json", "MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method": "tools/call", "Mcp-Name": "echo_note", "X-Connection": handle} {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var envelope struct {
			Result struct{ StructuredContent map[string]any }
		}
		_ = json.NewDecoder(resp.Body).Decode(&envelope)
		if resp.StatusCode != 200 || envelope.Result.StructuredContent["note"] != "hi" ||
			resp.Header.Get("MCP-Protocol-Version") != "2026-07-28" {
			t.Errorf("got %d %+v", resp.StatusCode, envelope)
		}

		for _, sent := range []map[string]string{{"Authorization": "Bearer " + handle}, {"X-Connection": "cn_unknown"},
			{"Authorization": "Bearer cn_unknown"}} {
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(call))
			for k, v := range map[string]string{"Content-Type": "application/json", "MCP-Protocol-Version": "2026-07-28",
				"Mcp-Method": "tools/call", "Mcp-Name": "echo_note"} {
				req.Header.Set(k, v)
			}
			for k, v := range sent {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			want := http.StatusUnauthorized
			if !strings.Contains(sent["Authorization"]+sent["X-Connection"], "unknown") {
				want = http.StatusOK
			}
			if resp.StatusCode != want ||
				(want == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") != `Bearer error="invalid_token"`) {
				t.Errorf("%v: got %d %q", sent, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
			}
		}
	})

	t.Run("HEAD carries the GET length and no content", func(t *testing.T) {
		get, err := http.Get(server.URL + "/api/openapi.json") // stable; /hello carries a timestamp of varying length
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(get.Body)
		get.Body.Close()
		head, err := http.Head(server.URL + "/api/openapi.json")
		if err != nil {
			t.Fatal(err)
		}
		head.Body.Close()
		if head.StatusCode != http.StatusOK || head.ContentLength != int64(len(content)) || len(content) == 0 {
			t.Errorf("HEAD %d Content-Length %d, GET sent %d bytes", head.StatusCode, head.ContentLength, len(content))
		}
	})

	t.Run("application_name and a bounded pool", func(t *testing.T) {
		c.call(http.MethodGet, "/api/hello", nil, nil, false)
		var n int
		if err := pool.QueryRow(t.Context(),
			"SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1",
			wantApplicationName).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n < 1 || n > 10 {
			t.Errorf("connections named %s: %d", wantApplicationName, n)
		}
	})

	t.Run("logout closes the session", func(t *testing.T) {
		if r := c.call(http.MethodPost, "/auth/logout", nil, nil, true); r.status != http.StatusForbidden {
			t.Errorf("without X-CSRF-Token: %d", r.status)
		}
		if r := c.post("/auth/logout", nil, nil); r.status != http.StatusNoContent {
			t.Fatalf("logout: %d %v", r.status, r.body)
		}
		if r := c.call(http.MethodGet, "/api/me", nil, nil, true); r.status != http.StatusUnauthorized {
			t.Errorf("the closed session: %d", r.status)
		}
	})
}

func TestAnExhaustedPoolAnswers503AfterTheAcquireTimeout(t *testing.T) {
	g, pool := deployed(t, 1, nil)
	held, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	started := time.Now()
	resp := serve(g, httptest.NewRequest(http.MethodGet, "/api/hello", nil))
	elapsed := time.Since(started)
	if code := problemCode(t, resp); resp.StatusCode != http.StatusServiceUnavailable || code != "pgmi.unavailable" || resp.Header.Get("Retry-After") != "1" {
		t.Errorf("got %d %s Retry-After %q", resp.StatusCode, code, resp.Header.Get("Retry-After"))
	}
	// Waited out once: a pool timeout is not a lost connection.
	if elapsed < acquireTimeout || elapsed > acquireTimeout+2*time.Second {
		t.Errorf("answered after %v, want about %v", elapsed, acquireTimeout)
	}
}

// TestLoad runs concurrent REST calls with and without interceptors and logs
// the throughput (go test -run Load -v). The routes are the e2e fixture's
// /intercept/echo (x-embed) and /api/hello; without the fixture only hello runs.
func TestLoad(t *testing.T) {
	const workers, perWorker = 16, 50
	scenarios := []struct {
		name     string
		pipeline *Pipeline
		path     string
		body     string
	}{
		{"no interceptors, /hello", nil, "/api/hello", ""},
		{"embed+request-id, /hello (not selected)", examplePipeline(t), "/api/hello", ""},
		{"no interceptors, /intercept/echo", nil, "/api/intercept/echo", `{"text":"load"}`},
		{"embed+request-id, /intercept/echo", examplePipeline(t), "/api/intercept/echo", `{"text":"load"}`},
	}
	for _, s := range scenarios {
		g, _ := deployed(t, 10, s.pipeline)
		server := httptest.NewServer(g.Routes())
		method := http.MethodGet
		if s.body != "" {
			method = http.MethodPost
		}
		probe := do(server.URL, method, s.path, s.body)
		if probe.status == http.StatusNotFound {
			server.Close()
			t.Logf("%-42s skipped: %s is not deployed", s.name, s.path)
			continue
		}
		var mu sync.Mutex
		var latencies []time.Duration
		var failures []string
		started := time.Now()
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				for range perWorker {
					begun := time.Now()
					r := do(server.URL, method, s.path, s.body)
					mu.Lock()
					latencies = append(latencies, time.Since(begun))
					if r.status != http.StatusOK {
						failures = append(failures, fmt.Sprintf("%d %s", r.status, r.body))
					}
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		wall := time.Since(started)
		server.Close()
		if len(failures) > 0 {
			t.Errorf("%s: %d failures, first %s", s.name, len(failures), failures[0])
		}
		slices.Sort(latencies)
		t.Logf("%-42s %d req %6.0f req/s  p50 %5.1f ms  p95 %5.1f ms", s.name, len(latencies),
			float64(len(latencies))/wall.Seconds(), ms(latencies[len(latencies)/2]), ms(latencies[len(latencies)*95/100]))
	}
}

func examplePipeline(t *testing.T) *Pipeline {
	t.Helper()
	p, err := buildPipeline([]string{"embed", "request-id"}, "fail", quiet)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// loadClient keeps connections open between calls, as a real client does.
var loadClient = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}}

type rawReply struct {
	status int
	body   string
}

func do(base, method, path, body string) rawReply {
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := loadClient.Do(req)
	if err != nil {
		return rawReply{0, err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return rawReply{resp.StatusCode, string(raw)}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// stalled is a channel whose dispatch never answers before the context ends.
type stalled struct{ *fakeChannel }

func (s stalled) RestInvoke(ctx context.Context, _ RequestContext, _ Policy, _ map[string]string, _ []byte) (RestResult, error) {
	<-ctx.Done()
	return RestResult{}, fmt.Errorf("rest invoke: %w", ctx.Err())
}

func (s stalled) MCPRequest(ctx context.Context, _ RequestContext, _ Policy, _ []byte, _ json.RawMessage) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, fmt.Errorf("mcp request: %w", ctx.Err())
}

func TestARequestPastItsDeadlineIs504(t *testing.T) {
	for name, g := range map[string]*Gateway{
		"the database cancelled it":  {Channel: &fakeChannel{errs: []error{nil, fmt.Errorf("rest invoke: %w", ErrDeadlineExceeded)}}},
		"the gateway's own deadline": {Channel: stalled{&fakeChannel{}}, Deadline: 20 * time.Millisecond},
	} {
		resp := serve(g, httptest.NewRequest(http.MethodGet, "/api/slow", nil))
		if code := problemCode(t, resp); resp.StatusCode != http.StatusGatewayTimeout || code != "pgmi.deadline_exceeded" ||
			!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/problem+json") || resp.Header.Get("X-Request-Id") == "" {
			t.Errorf("rest, %s: %d %s %v", name, resp.StatusCode, code, resp.Header)
		}
	}
	for name, g := range map[string]*Gateway{
		"the database cancelled it":  {Channel: &fakeChannel{errs: []error{nil, fmt.Errorf("mcp request: %w", ErrDeadlineExceeded)}}},
		"the gateway's own deadline": {Channel: stalled{&fakeChannel{}}, Deadline: 20 * time.Millisecond},
	} {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"slow"}}`))
		r.Header.Set("Content-Type", "application/json")
		resp := serve(g, r)
		var answer struct {
			ID    int
			Error struct {
				Code int
				Data struct{ Code, RequestID string }
			}
		}
		if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil || resp.StatusCode != http.StatusGatewayTimeout ||
			answer.ID != 7 || answer.Error.Code != -32603 || answer.Error.Data.Code != "pgmi.deadline_exceeded" || answer.Error.Data.RequestID == "" {
			t.Errorf("mcp, %s: %d %+v %v", name, resp.StatusCode, answer, err)
		}
	}
}

func TestTheDatabaseCancelsAStatementPastTheDeadline(t *testing.T) {
	pool, err := NewPool(t.Context(), databaseURL(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &Postgres{Pool: pool, AcquireTimeout: acquireTimeout}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	const setting = `SELECT setting::int FROM pg_settings WHERE name = 'statement_timeout'`
	var ms int
	if err := db.policyRow(ctx, setting, nil, &ms); err != nil || ms <= 0 || ms > 1000 {
		t.Fatalf("statement_timeout %d ms: %v", ms, err)
	}
	started := time.Now()
	err = db.policyRow(ctx, `SELECT pg_sleep(5)::text`, nil, new(string))
	if !errors.Is(err, ErrDeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Errorf("after %v: %v", time.Since(started), err)
	}
	if err := db.policyRow(t.Context(), setting, nil, &ms); err != nil || ms != 0 {
		t.Errorf("the pooled connection kept %d ms: %v", ms, err)
	}
}

type renameTool struct{}

func (renameTool) Spec() Spec                  { return Spec{Name: "rename", NeedsJSON: true, Timeout: time.Second} }
func (renameTool) Applies(RequestContext) bool { return true }
func (renameTool) OnRequest(_ context.Context, _ RequestContext, req *Request) error {
	req.JSON.(map[string]any)["params"].(map[string]any)["name"] = "rewritten_tool"
	return nil
}

type recordingChannel struct {
	fakeChannel
	mcpHeaders []MCPHeaders
}

func (c *recordingChannel) MCPPolicy(_ context.Context, _ RequestContext, _ []byte, _ string, h MCPHeaders) (Policy, error) {
	c.mcpHeaders = append(c.mcpHeaders, h)
	return Policy{Modern: true}, nil
}

func TestARouteChangingInterceptorRestatesTheMCPHeaders(t *testing.T) {
	channel := &recordingChannel{}
	g := &Gateway{Channel: channel, Pipeline: &Pipeline{Requests: []RequestInterceptor{renameTool{}}, Log: quiet}, Log: quiet}
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo_note","arguments":{}}}`))
	for name, value := range map[string]string{"Content-Type": "application/json", "MCP-Protocol-Version": "2026-07-28",
		"Mcp-Method": "tools/call", "Mcp-Name": "echo_note", "X-Connection": "cn_test"} {
		req.Header.Set(name, value)
	}
	serve(g, req)
	if len(channel.mcpHeaders) != 2 {
		t.Fatalf("want the policy resolved twice, got %d", len(channel.mcpHeaders))
	}
	second := channel.mcpHeaders[1]
	if deref(second.Method) != "tools/call" || deref(second.Name) != "rewritten_tool" || deref(second.ProtocolVersion) != "2026-07-28" {
		t.Errorf("second policy call: method %q name %q version %q", deref(second.Method), deref(second.Name), deref(second.ProtocolVersion))
	}
}
