# Go reference gateway

Demo code from pgmi, a baseline to learn from, not a library. Read it, then
write your own gateway that fits this project. The wire rules are in
[../PROTOCOL.md](../PROTOCOL.md); every place that needs a project decision is
marked `ADAPT`.

Go 1.26, the standard library's `net/http` (method-and-path `ServeMux`
patterns, `log/slog`), pgx v5 `pgxpool` for the pool,
`github.com/coreos/go-oidc` and `golang.org/x/oauth2` for sign-in,
`github.com/go-jose/go-jose` for access-token checks, `golang.org/x/sync`
for `errgroup`. One package, `main`, one file per concern:

| File | What |
|---|---|
| `main.go` | wiring only: reads the environment (a bad number is a startup error), composes the seams, listens |
| `gateway.go` | routes, the middleware every route shares, REST under `/api`; every error answered in `writeError()` |
| `mcp.go` | `POST /mcp`, both protocol eras; its errors answered in `mcpError()` |
| `channel.go` | every database call behind the `Channel` interface, and its decorators |
| `interceptors.go` | the request and response interceptor pipeline, and two examples |
| `login.go` | OIDC sign-in, dev sign-in, logout, session and CSRF cookies |
| `resource.go` | `/mcp` as an OAuth resource server: metadata, token checks, the key cache, rate limits |
| `*_test.go` | unit tests of each; database tests with `DATABASE_URL` |

## Run it

```bash
DATABASE_URL="postgresql://mydb_gateway:$GATEWAY_PASSWORD@localhost:5432/mydb" go run .
go test -race ./...    # the database tests run when DATABASE_URL is set; -race needs cgo and a C compiler
```

Settings are the environment variables of `../PROTOCOL.md` ("Configuration").

## How a request flows

`Gateway.Routes()` returns the `http.Handler`: a `ServeMux` with
`"POST /mcp"`, `"GET /auth/login"` and the rest, wrapped in middleware
(`func(http.Handler) http.Handler`): the request deadline, the access log,
the `Origin` check and, on `/mcp`, the per-address rate limit. `/api` goes
around the `ServeMux`, which would clean `/api//x` and redirect it: the path
under `/api` reaches the database exactly as sent (`../PROTOCOL.md`, "The
path is the database's").

A handler returns its error instead of answering it (`handlerFunc`).
`writeError()` maps each error to its problem in one place:
`*problem` as it is, `*RetriesExhaustedError` to 409, `ErrUnavailable` to
503, `*InterceptorFailedError` to 502, anything else to a logged 500 that
never shows its text. `/mcp` answers JSON-RPC, so it has its twin,
`mcpError()`.

Every request carries a `context.Context` with a 30 s deadline; the database
calls, the retry backoff and every interceptor stop when it ends. Pass it to
every outgoing call you add.

Outgoing HTTP (the identity provider, an embedding API) goes through the one
`http.Client` built in `main`: it pools its connections and is safe to share.

## Calling a new handler

REST and MCP need no gateway change: a new REST route is reachable at
`/api/<its path>` and a new MCP tool through `tools/call`, because the
gateway forwards to `api.rest_invoke` and `api.mcp_handle_request` and the
database routes. A client calls it like any other route:

```go
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/projects", strings.NewReader(`{"name":"Apollo"}`))
req.Header.Set("Content-Type", "application/json")
req.Header.Set("X-CSRF-Token", csrf)                     // the __Host-csrf cookie's value
req.AddCookie(&http.Cookie{Name: "__Host-session", Value: session})
resp, err := client.Do(req)
// ...
if resp.StatusCode >= 400 {
	var problem struct{ Code string } // application/problem+json
	_ = json.NewDecoder(resp.Body).Decode(&problem)
	// branch on problem.Code, never on title or detail
}
```

A new database entry point (RPC through `api.rpc_route_policy` and
`api.rpc_invoke`, say) is one more method on `Channel` in `channel.go`,
implemented on `*Postgres` with the same policy-then-dispatch pattern. The
decorators embed the `Channel` they wrap, so one that does not change the
new call passes it through without a line of code.

## The two seams

Both are in `../PROTOCOL.md` ("Extension seams").

### The database channel

`main` composes it once; every request goroutine shares it:

```go
channel := ConnectionRetry{Attempts: 2, Log: log,
	Channel: SerializationRetry{Attempts: attempts, BaseDelay: baseDelay,
		Channel: Tracing{Log: log, Channel: db}}}
```

A decorator is a struct that embeds `Channel` and overrides only the calls
it changes:

```go
type Timing struct {
	Channel
	Record func(op string, d time.Duration)
}

func (t Timing) RestInvoke(ctx context.Context, rc RequestContext, p Policy, h map[string]string, body []byte) (RestResult, error) {
	started := time.Now()
	defer func() { t.Record("rest.invoke", time.Since(started)) }()
	return t.Channel.RestInvoke(ctx, rc, p, h, body)
}
```

`ConnectionRetry` shows the rule every retry keeps: a call is sent again only
when it cannot apply anything twice (a policy lookup, a `readOnly` route). A
write never is: the connection can drop after the commit. A pool timeout
(`ErrUnavailable`) is not a dropped connection; it is answered 503 at once.

### Interceptors

`INTERCEPTORS=embed,request-id` runs the two examples; unset, none run and
every body passes as the bytes the client sent. An interceptor implements
`RequestInterceptor` or `ResponseInterceptor`:

```go
type SignURLs struct{ Signer Signer } // the key comes from configuration, never logged

func (SignURLs) Spec() Spec {
	// NeedsJSON: reads resp.JSON, parsed once for all. Fail: an unsigned
	// URL is a broken answer, so 502. The timeout is bounded by the deadline.
	return Spec{Name: "sign-urls", NeedsJSON: true, Failure: Fail, Timeout: 2 * time.Second}
}

func (SignURLs) Applies(rc RequestContext) bool {
	return rc.Handler == "list_files" // ADAPT: handler names, as the database publishes them
}

func (s SignURLs) OnResponse(ctx context.Context, rc RequestContext, resp *Response) error {
	body, _ := resp.JSON.(map[string]any)
	files, _ := body["files"].([]any)
	for _, f := range files {
		if item, ok := f.(map[string]any); ok {
			url, err := s.Signer.Sign(ctx, fmt.Sprint(item["url"]), 5*time.Minute)
			if err != nil {
				return err
			}
			item["url"] = url
		}
	}
	return nil
}
```

Add it in `buildPipeline()` in `main.go`. A request interceptor is the same
with `OnRequest(ctx, rc, *Request)`; it runs after the policy call and before
the transaction opens, and may change the method, URL, headers and body.
Change the route and the gateway resolves the policy again (the context then
carries the new handler); the session handles are set again after it,
whatever it did.

Embeddings from a real model: an `Embedder` over the shared client.
`EmbedFields` embeds every marked field concurrently (`errgroup`).

```go
type ModelEmbedder struct {
	Client      *http.Client // the one from main
	URL, Key    string
}

func (m ModelEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	body, _ := json.Marshal(map[string]any{"model": "text-embedding-3-small", "input": text})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.URL+"/embeddings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.Key)
	resp, err := m.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API answered %d", resp.StatusCode)
	}
	var out struct{ Data []struct{ Embedding []float64 } }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Data) == 0 {
		return nil, fmt.Errorf("embedding API answer: %v", err)
	}
	return out.Data[0].Embedding, nil
}

// in buildPipeline:
p.Requests = append(p.Requests, EmbedFields{Embedder: ModelEmbedder{client, url, key}, Failure: Degrade, Timeout: 2 * time.Second})
```

Then mark the property in the handler's `inputSchema`:
`"query": {"type": "string", "x-embed": true}, "query_embedding": {"type": "array"}`.

The rules: an interceptor keeps no per-request state (it is in the message
and `rc`), selects itself from `rc` (the handler and its `x-*` annotations,
never a path list), and is `Fail` unless the request is still right without
it (`Degrade`). Its timeout is enforced even when it ignores `ctx`, but an
abandoned goroutine still runs to its end: pass `ctx` to every outgoing call.

## OAuth and sign-in

### MCP clients with OAuth

`MCP_RESOURCE=https://mcp.example.com/mcp MCP_AUTHORIZATION_SERVERS=https://login.example.com`
turns it on (`../PROTOCOL.md`, "MCP clients with OAuth"). `mcpCaller()` in
`mcp.go` calls `Resource.Verify(...)` before reading the body and puts only
the verified issuer, subject and client id into the MCP context. A
connection handle (`Authorization: Bearer cn_...`, or `X-Connection` without
another `Authorization`) goes to the database as it is, OAuth on or off; one
the database does not know is 401 (`handleUnknown`). The places to adapt are marked
`ADAPT` in `resource.go`.

### Sign-in

With `OIDC_ISSUER`, `OIDC_CLIENT_ID` and `OIDC_REDIRECT_URI` set, `login.go`
runs the authorization code flow with PKCE (`golang.org/x/oauth2`) and checks
the ID token with go-oidc, then by hand for what the library leaves to the
caller: the nonce, `azp` when there are several audiences, `iat`, and the
60 s of leeway. The pending sign-ins live in this process's memory
(`pendingLogins`): several processes behind a load balancer need a shared
store or sticky sessions (`ADAPT`). `DEV_LOGIN=1` serves
`POST /auth/dev-login` for an `env=dev` deploy; never in production.

`sslmode` in `DATABASE_URL` is pgx's, which follows libpq: `require`
encrypts, and verifies against `sslrootcert` when one is given;
`verify-ca` and `verify-full` check the certificate (and the host name).

## What to adapt

Every place is marked `ADAPT` in the code.

| What | Where |
|---|---|
| the name in `pg_stat_activity`, the pool size | `ApplicationName` in `gateway.go`, the pool in `main.go` |
| the channel decorators | `main.go` |
| your interceptors | `buildPipeline()` in `main.go` |
| RPC, if you serve it | `Channel` in `channel.go` |
| trace lines to OpenTelemetry | `Tracing` in `channel.go` |
| cookie names and lifetimes, the development issuer; pending sign-ins across processes | `login.go` |
| client ids that are never agents; the client address behind a reverse proxy | `resource.go` |
