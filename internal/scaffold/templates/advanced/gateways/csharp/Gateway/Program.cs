// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library. Namespaces, project and type names are
// examples: rename them to match the project. The wire rules it keeps are in
// ../../PROTOCOL.md; README.md says how to run it and where to adapt it.
//
//   DATABASE_URL="postgresql://mydb_gateway:$GATEWAY_PASSWORD@localhost:5432/mydb" dotnet run --project Gateway
//
// The composition root: every service is registered here, once, and every
// endpoint is mapped here with the filters that guard it.
//
//   ANY  /api/...          REST: api.rest_invoke                  RestEndpoint.cs
//   POST /mcp              MCP, both eras: api.mcp_handle_request McpEndpoint.cs
//   GET  /.well-known/oauth-protected-resource[/mcp]               ResourceServer.cs
//   GET  /auth/login, /auth/callback; POST /auth/logout, /auth/dev-login   SignIn.cs
//   GET  /health; anything else is 404 pgmi.not_found

using Microsoft.Extensions.Options;
using Pgmi.Gateway;

var builder = WebApplication.CreateBuilder(args);

// Options from configuration: environment variables by default, so the keys
// are the ones every reference gateway reads (GatewayOptions.cs).
builder.Services.AddOptions<GatewayOptions>().Bind(builder.Configuration).ValidateOnStart();
builder.Services.AddSingleton<IValidateOptions<GatewayOptions>, GatewayOptionsValidator>();
var listen = builder.Configuration.Get<GatewayOptions>() ?? new GatewayOptions();
builder.WebHost.UseUrls($"http://{listen.Host}:{listen.Port}");
builder.WebHost.ConfigureKestrel(kestrel =>
{
    kestrel.AddServerHeader = false;
    kestrel.Limits.MaxRequestBodySize = GatewayOptions.MaxBodyBytes;
});

// Hosting logs every request URL at Information, and a callback's query
// carries the authorization code: keep the framework at Warning.
builder.Logging.AddFilter("Microsoft.AspNetCore", LogLevel.Warning);
builder.Logging.AddSimpleConsole(console => console.SingleLine = true);

builder.Services.AddProblemDetails(problems => problems.CustomizeProblemDetails = Problems.Customize);
builder.Services.AddExceptionHandler<GatewayExceptionHandler>();
builder.Services.AddSingleton(TimeProvider.System);

// The database channel (DbChannel.cs), decorated once. ADAPT: the decorators
// your gateway needs; both retries wrap the tracing, so each attempt is a
// log line of its own.
builder.Services.AddSingleton(sp =>
{
    var o = sp.GetRequiredService<IOptions<GatewayOptions>>().Value;
    return Database.CreateDataSource(o.DatabaseUrl, o.PoolMaxSize, sp.GetRequiredService<ILoggerFactory>());
});
builder.Services.AddSingleton<IDbChannel>(sp =>
{
    var o = sp.GetRequiredService<IOptions<GatewayOptions>>().Value;
    return new ConnectionRetry(
        new SerializationRetry(
            new TracingChannel(new PostgresChannel(sp.GetRequiredService<Npgsql.NpgsqlDataSource>()),
                               sp.GetRequiredService<ILogger<TracingChannel>>()),
            o.RetryMaxAttempts, o.RetryBaseDelaySeconds),
        sp.GetRequiredService<ILogger<ConnectionRetry>>());
});

// Interceptors (Interceptors.cs): keyed by the name INTERCEPTORS uses, run in
// its order. ADAPT: register yours the same way, with what they depend on.
builder.Services.AddSingleton<IEmbedder, FakeEmbedder>();
builder.Services.AddKeyedSingleton<IRequestInterceptor, EmbedFields>("embed");
builder.Services.AddKeyedSingleton<IResponseInterceptor, RequestIdHeader>("request-id");
builder.Services.AddKeyedSingleton<IRequestInterceptor, HeaderNames>("header-names");
builder.Services.AddSingleton(sp =>
{
    var requests = new List<IRequestInterceptor>();
    var responses = new List<IResponseInterceptor>();
    foreach (var name in sp.GetRequiredService<IOptions<GatewayOptions>>().Value.InterceptorNames)
    {
        var onRequest = sp.GetKeyedService<IRequestInterceptor>(name);
        var onResponse = sp.GetKeyedService<IResponseInterceptor>(name);
        if (onRequest is null && onResponse is null)
        {
            throw new InvalidOperationException($"unknown interceptor {name}; known: embed, request-id, header-names");
        }
        if (onRequest is not null) requests.Add(onRequest);
        if (onResponse is not null) responses.Add(onResponse);
    }
    return new InterceptorPipeline(requests, responses, sp.GetRequiredService<ILogger<InterceptorPipeline>>());
});

// Identity. Every outgoing call goes through IHttpClientFactory: pooled,
// with a timeout. ADAPT: add DelegatingHandlers here (README.md).
builder.Services.AddHttpClient(IssuerKeys.HttpClientName, client => client.Timeout = TimeSpan.FromSeconds(5));
builder.Services.AddSingleton<SessionCookies>();
builder.Services.AddSingleton<LoginSessions>();
builder.Services.AddSingleton<OidcSignIn>();
builder.Services.AddSingleton<ResourceServer>();
builder.Services.AddSingleton<RateLimits>();

var app = builder.Build();

// Fail at startup, not on the first request, and with exit code 1: an
// invalid option, an unknown interceptor name, a DATABASE_URL whose TLS
// settings Database.cs refuses.
try
{
    app.Services.GetRequiredService<InterceptorPipeline>();
    app.Services.GetRequiredService<Npgsql.NpgsqlDataSource>();
}
catch (Exception e) when (e is ArgumentException or InvalidOperationException or OptionsValidationException)
{
    Console.Error.WriteLine($"Error: {e.Message}");
    return 1;
}

app.UseExceptionHandler();

app.MapGet("/health", () => Results.Json(new { status = "healthy" }));

var rest = app.MapGroup(RawRestPrefixFilter.Prefix)
    .AddEndpointFilter<RawRestPrefixFilter>()
    .AddEndpointFilter<OriginFilter>()
    .AddEndpointFilter<CsrfFilter>();
rest.Map("{**path}", RestEndpoint.HandleAsync);

app.MapPost("/mcp", McpEndpoint.HandleAsync)
    .AddEndpointFilter<PerIpRateLimitFilter>()
    .AddEndpointFilter<OriginFilter>();
app.MapMethods("/mcp", ["GET", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS"], McpEndpoint.MethodNotAllowed);

app.MapGet("/.well-known/oauth-protected-resource/{**rest}", (HttpContext http, ResourceServer resourceServer) =>
    resourceServer.Enabled && resourceServer.MetadataPaths.Contains(http.Request.Path.Value!)
        ? Results.Json(resourceServer.Metadata())
        : Problems.NotFound());

var auth = app.MapGroup("/auth");
auth.MapGet("/login", SignInEndpoints.LoginAsync).AddEndpointFilter<PerIpRateLimitFilter>();
auth.MapGet("/callback", SignInEndpoints.CallbackAsync).AddEndpointFilter<PerIpRateLimitFilter>();
auth.MapPost("/logout", SignInEndpoints.LogoutAsync).AddEndpointFilter<OriginFilter>().AddEndpointFilter<CsrfFilter>();
auth.MapPost("/dev-login", SignInEndpoints.DevLoginAsync).AddEndpointFilter<OriginFilter>();

// Kestrel removes dot segments before routing, so /api/%2e%2e/x routes here
// although its raw target is REST: the raw prefix decides, as in the group.
app.MapFallback(RestEndpoint.HandleAsync)
    .AddEndpointFilter<RawRestPrefixFilter>()
    .AddEndpointFilter<OriginFilter>()
    .AddEndpointFilter<CsrfFilter>();

app.Run();
return 0;

// For WebApplicationFactory<Program> in Gateway.Tests.
public partial class Program;
