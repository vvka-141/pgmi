# C# reference gateway

Demo code from pgmi, a baseline to learn from, not a library. Read it, then
write your own gateway that fits this project. The wire rules are in
[../PROTOCOL.md](../PROTOCOL.md); every place that needs a project decision is
marked `ADAPT`.

ASP.NET Core minimal APIs on .NET 10, Npgsql's `NpgsqlDataSource` for the
pool, `Microsoft.IdentityModel` for OIDC discovery, keys and JWT checks. It is
written the way an ASP.NET Core service is usually written: services in DI,
options bound from configuration, endpoint filters for the browser rules,
`IExceptionHandler` with `AddProblemDetails`, `IHttpClientFactory` for every
outgoing call, `ILogger<T>`, a `CancellationToken` through every call.

| File | What |
|---|---|
| `Gateway/Program.cs` | the composition root: every service and every endpoint, with its filters |
| `Gateway/GatewayOptions.cs` | the settings, bound from the environment, checked at startup |
| `Gateway/RestEndpoint.cs` | REST under `/api`: policy, interceptors, `api.rest_invoke` |
| `Gateway/McpEndpoint.cs` | MCP on `/mcp`, both eras: caller, policy, interceptors, `api.mcp_handle_request` |
| `Gateway/DbChannel.cs` | every database call behind `IDbChannel`, and `PostgresChannel` |
| `Gateway/ChannelDecorators.cs` | tracing, serialization retry, connection retry |
| `Gateway/Interceptors.cs` | the request and response interceptor pipeline |
| `Gateway/ExampleInterceptors.cs` | `embed` (with the fake embedder) and `request-id` |
| `Gateway/BrowserRules.cs` | session and CSRF cookies, forwarded headers, the Origin, CSRF and raw-prefix filters |
| `Gateway/SignIn.cs` | OIDC sign-in, development sign-in, logout |
| `Gateway/ResourceServer.cs` | `/mcp` as an OAuth resource server, and the rate limits |
| `Gateway/IssuerKeys.cs` | one issuer's discovery document and signing keys, cached |
| `Gateway/Problems.cs` | the problems the gateway answers itself, and the exception handler |
| `Gateway/Database.cs` | `DATABASE_URL` to an Npgsql data source |
| `Gateway.Tests/` | unit tests, in-memory HTTP tests (`WebApplicationFactory`), database tests with `DATABASE_URL` |

## Run it

```bash
DATABASE_URL="postgresql://mydb_gateway:$GATEWAY_PASSWORD@localhost:5432/mydb" dotnet run --project Gateway
dotnet test Gateway.Tests        # the database tests run when DATABASE_URL is set
dotnet list Gateway package --vulnerable --include-transitive
```

Without a local .NET SDK, the official image does the same (pgmi's CI runs it
this way): `docker run --rm --network host -e DATABASE_URL -v "$PWD:/src" -w /src
mcr.microsoft.com/dotnet/sdk:10.0.403 dotnet test Gateway.Tests`.

Settings are the environment variables of `../PROTOCOL.md` ("Configuration").
`GatewayOptions` binds them with `[ConfigurationKeyName]`, so they can come
from `appsettings.json` or any other configuration source too, and a bad one
(`EMBED_FAILURE_POLICY=ignore`, an unknown interceptor, a plaintext issuer)
stops the gateway at startup. The per-call trace lines are Information
logs of `Pgmi.Gateway.TracingChannel`; `Logging__LogLevel__Pgmi.Gateway.TracingChannel=Warning`
turns them off.

## How a request flows

Routing picks the endpoint; its filters run first (`PerIpRateLimitFilter`,
`OriginFilter`, `CsrfFilter`, `RawRestPrefixFilter`) and answer a problem when
they refuse. The endpoint reads the body, calls `IDbChannel` and returns an
`IResult`: `DatabaseAnswer` for REST (the database's status, headers and
bytes), JSON for MCP. Expected outcomes (retries exhausted, an interceptor
failed, no pooled connection on `/mcp`) are caught where the answer's shape
is known; anything else reaches `GatewayExceptionHandler`, which answers 413,
503 or a sanitized 500.

`HttpContext.RequestAborted` is the request's token: it reaches every
database call, the retry backoff and every interceptor. Pass it to anything
you add.

## Calling a new handler

REST and MCP need no gateway change: a new REST route is reachable at
`/api/<its path>` and a new MCP tool through `tools/call`, because the gateway
forwards to `api.rest_invoke` and `api.mcp_handle_request` and the database
routes. A client calls it like any other route:

```csharp
using var request = new HttpRequestMessage(HttpMethod.Post, "/api/projects")
{
    Content = JsonContent.Create(new { name = "Apollo" }),
};
request.Headers.Add("Cookie", $"__Host-session={session}");
request.Headers.Add("X-CSRF-Token", csrf);
using var response = await http.SendAsync(request, ct);
if (!response.IsSuccessStatusCode)
{
    var problem = await response.Content.ReadFromJsonAsync<JsonElement>(ct);   // application/problem+json
    // branch on problem.GetProperty("code"), never on title or detail
}
```

A new database entry point (RPC through `api.rpc_route_policy` and
`api.rpc_invoke`, say) is one more method on `IDbChannel`, implemented in
`PostgresChannel` with the same policy-then-dispatch pattern, a virtual
pass-through on `DbChannelDecorator`, and an endpoint mapped in `Program.cs`.

## The two seams

Both are in `../PROTOCOL.md` ("Extension seams").

### The database channel

`Program.cs` composes it once, as a singleton:

```csharp
new ConnectionRetry(new SerializationRetry(new TracingChannel(new PostgresChannel(dataSource), logger), attempts, delay), logger)
```

A decorator derives from `DbChannelDecorator` and overrides only the calls it
changes. `ConnectionRetry` shows the rule every retry keeps: a call is sent
again only when it cannot apply anything twice (a policy lookup, a
`readOnly` route). A write never is. A cache of the policy, metrics or a test
double is one more decorator in that line.

### Interceptors

`INTERCEPTORS=embed,request-id` runs the two examples, in that order; unset,
none run and every body passes as the client's bytes. An interceptor is a
singleton implementing `IRequestInterceptor` or `IResponseInterceptor`,
registered as a keyed service under the name `INTERCEPTORS` uses:

```csharp
builder.Services.AddKeyedSingleton<IResponseInterceptor, SignUrls>("sign-urls");
```

It selects itself in `AppliesTo(ctx)` from what the database published about
the handler (`ctx.Handler`, `ctx.Annotations`), declares `NeedsJson`,
`Failure` (`Fail` unless the request is still right without it) and a
`Timeout`, and keeps no per-request state.

Embeddings from a real model. The outgoing call goes through
`IHttpClientFactory`: a typed client, pooled, with a `DelegatingHandler` for
the key and the resilience handler for retries
(`Microsoft.Extensions.Http.Resilience`, pinned like the other packages):

```csharp
public sealed class ModelEmbedder(HttpClient http) : IEmbedder
{
    public async Task<IReadOnlyList<double[]>> EmbedAsync(IReadOnlyList<string> texts, CancellationToken ct)
    {
        using var response = await http.PostAsJsonAsync("embeddings", new { model = "text-embedding-3-small", input = texts }, ct);
        response.EnsureSuccessStatusCode();
        var body = await response.Content.ReadFromJsonAsync<JsonElement>(ct);
        return body.GetProperty("data").EnumerateArray()
                   .Select(item => item.GetProperty("embedding").Deserialize<double[]>()!).ToList();
    }
}

// The key comes from configuration and is never logged.
public sealed class EmbeddingKey(IConfiguration configuration) : DelegatingHandler
{
    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken ct)
    {
        request.Headers.Authorization = new("Bearer", configuration["EMBED_KEY"]);
        return base.SendAsync(request, ct);
    }
}

builder.Services.AddTransient<EmbeddingKey>();
builder.Services.AddHttpClient<IEmbedder, ModelEmbedder>(http => http.BaseAddress = new Uri(builder.Configuration["EMBED_URL"]!))
    .AddHttpMessageHandler<EmbeddingKey>()
    .AddStandardResilienceHandler();
```

Remove the `FakeEmbedder` registration, set `EMBED_FAILURE_POLICY=degrade` if
the handler can answer without the vector, and mark the property in the
handler's `inputSchema`:
`"query": {"type": "string", "x-embed": true}, "query_embedding": {"type": "array"}`.
The resilience handler retries an embedding call, which is safe: it runs
before the transaction opens. Never retry an outgoing call that has a side
effect without an idempotency key.

Signed storage URLs, after the commit:

```csharp
public sealed class SignUrls(IUrlSigner signer) : IResponseInterceptor
{
    public string Name => "sign-urls";
    public bool NeedsJson => true;
    public FailurePolicy Failure => FailurePolicy.Fail;       // an unsigned URL is a broken answer
    public TimeSpan Timeout => TimeSpan.FromSeconds(2);

    public bool AppliesTo(RequestContext ctx) => ctx.Handler is "list_files";   // ADAPT: handler names, as the database publishes them

    public async ValueTask OnResponseAsync(InterceptedResponse response, RequestContext ctx, CancellationToken ct)
    {
        foreach (var file in response.Json?["files"]?.AsArray() ?? [])
        {
            file!["url"] = await signer.SignAsync(file["url"]!.GetValue<string>(), TimeSpan.FromMinutes(5), ct);
        }
    }
}
```

## OAuth and sign-in

### MCP clients with OAuth

`MCP_RESOURCE=https://mcp.example.com/mcp MCP_AUTHORIZATION_SERVERS=https://login.example.com`
turns it on (`../PROTOCOL.md`, "MCP clients with OAuth"). `McpEndpoint` calls
`ResourceServer.VerifyAsync` before reading the body and puts only
`VerifiedClient.Context` into the MCP context. A connection handle
(`Authorization: Bearer cn_...`, or `X-Connection` without another
`Authorization`) goes to the database as it is, OAuth on or off; one the
database does not know is 401 (`McpEndpoint.HandleUnknown`). The places to adapt are marked `ADAPT` in
`ResourceServer.cs` (the client address behind your reverse proxy comes
through `UseForwardedHeaders`).

### Sign-in

`OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URI`
turn on `/auth/login` and `/auth/callback` (`SignIn.cs`). It is not
`AddOpenIdConnect()`: the session is the database's handle in
`__Host-session` and every refusal is a `login.*` problem, which that
handler's own cookies and error pages would fight. Pending sign-ins live in
the process: with several gateway processes, use a shared store or sticky
sessions (`PendingLogins`, ADAPT). `DEV_LOGIN=1` serves
`POST /auth/dev-login` for an `env=dev` deploy; never in production.

## What to adapt

Every place is marked `ADAPT` in the code.

| What | Where |
|---|---|
| the name in `pg_stat_activity`, the pool | `Database.cs` |
| the settings and their source | `GatewayOptions.cs` |
| the channel decorators | `Program.cs`; a new one in `ChannelDecorators.cs` |
| your interceptors | keyed singletons in `Program.cs` |
| outgoing HTTP: handlers, resilience | `Program.cs` (`IHttpClientFactory`) |
| RPC, if you serve it | `IDbChannel` in `DbChannel.cs` |
| trace lines to OpenTelemetry (`ActivitySource`) | `TracingChannel` in `ChannelDecorators.cs` |
| cookie names and lifetime | `BrowserRules.cs` |
| pending sign-ins across processes | `PendingLogins` in `SignIn.cs` |
| client ids that are never agents; the client address behind a reverse proxy | `ResourceServer.cs` |
