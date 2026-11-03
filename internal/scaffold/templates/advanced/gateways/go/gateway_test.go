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
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
		"__Host-session=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax": "",
		"__Host-csrf=; Path=/; Max-Age=0; Secure; SameSite=Lax":              "",
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
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return reply{resp.StatusCode, resp.Header, decoded}
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
		if opened.status != http.StatusCreated {
			t.Fatalf("open organization session: %d %v", opened.status, opened.body)
		}
		organizationSession, _ = opened.body["organizationSession"].(string)
		assertProblem(t, c.post("/api/connections", map[string]any{"clientName": "x", "validDays": 999,
			"organizationId": organizationID, "roleId": "agent.reader"},
			map[string]string{"x-organization-session": organizationSession}), 422, "membership.connection.validity_invalid")
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
