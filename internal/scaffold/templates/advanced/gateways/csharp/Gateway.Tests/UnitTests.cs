// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own tests that fit this project.
//
// The rules that need no server and no database: header forwarding, the CSRF
// token, the rate limiter, the channel decorators' retry rules, the
// interceptor pipeline and the example embedder.

using System.Net.Sockets;
using System.Text;
using System.Text.Json;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Npgsql;
using Xunit;

namespace Pgmi.Gateway.Tests;

public class HeaderAndCookieRules
{
    [Fact]
    public void TheLoginSessionComesFromTheCookieOnly()
    {
        var headers = new HeaderDictionary { ["X-Login-Session"] = "ls_forged", ["X-Connection"] = "cn_x", ["Accept"] = "application/json" };
        var forwarded = ForwardedHeaders.From(headers, "ls_real");
        Assert.Equal("ls_real", forwarded["x-login-session"]);
        Assert.Equal("application/json", forwarded["accept"]);
        Assert.False(forwarded.ContainsKey("x-connection"));
    }

    [Fact]
    public void AnInterceptorCannotSetTheSession()
    {
        var reset = ForwardedHeaders.Reset(new Dictionary<string, string> { ["X-Login-Session"] = "ls_forged", ["X-Trace"] = "t" }, null);
        Assert.Equal(new Dictionary<string, string> { ["x-trace"] = "t" }, reset);
    }

    [Fact]
    public void ARepeatedListHeaderIsJoined() =>
        Assert.Equal("application/json, */*", ForwardedHeaders.From(new HeaderDictionary { ["Accept"] = new(["application/json", "*/*"]) }, null)["accept"]);

    [Theory]
    [InlineData("AUTHORIZATION")]
    [InlineData("content-type")]
    [InlineData("Origin")]
    [InlineData("x-csrf-token")]
    [InlineData("X-Organization-Session")]
    [InlineData("X-PGMI-Transaction-Isolation")]
    public void ARepeatedSingleValuedHeaderIsRefused(string name) =>
        Assert.Throws<HeaderCollisionException>(() => ForwardedHeaders.From(new HeaderDictionary { [name] = new(["a", "b"]) }, null));

    [Theory]
    [InlineData("/api", "/")]
    [InlineData("/api?x=1", "/?x=1")]
    [InlineData("/api/hello?name=a%20b", "/hello?name=a%20b")]
    [InlineData("/api/a%2Fb", "/a%2Fb")]
    [InlineData("/API/hello", null)]
    [InlineData("/%61pi/hello", null)]
    [InlineData("/apix", null)]
    public void TheRestPrefixMatchesTheRawPathCaseSensitively(string rawTarget, string? expected) =>
        Assert.Equal(expected, RawRestPrefixFilter.RestUrl(rawTarget));

    [Theory]
    [InlineData("Repeatable_Read", "repeatable read")]
    [InlineData("read-uncommitted", "read committed")]
    [InlineData("snapshot", null)]
    public void IsolationNamesAreCaseAndSeparatorFree(string input, string? expected) =>
        Assert.Equal(expected, PostgresChannel.NormalizeIsolation(input));

    [Fact]
    public void AStateChangingRequestWithTheSessionNeedsItsCsrfToken()
    {
        var cookies = new SessionCookies(Options.Create(new GatewayOptions { CsrfSecret = "s" }));
        Assert.True(cookies.CsrfOk("GET", "ls_a", null));
        Assert.True(cookies.CsrfOk("POST", null, null));
        Assert.False(cookies.CsrfOk("POST", "ls_a", null));
        Assert.False(cookies.CsrfOk("POST", "ls_a", cookies.CsrfToken("ls_b")));
        Assert.True(cookies.CsrfOk("POST", "ls_a", cookies.CsrfToken("ls_a")));
        Assert.Equal(cookies.CsrfToken("ls_a"), new SessionCookies(Options.Create(new GatewayOptions { CsrfSecret = "s" })).CsrfToken("ls_a"));
    }

    [Theory]
    [InlineData("/app?tab=2", "/app?tab=2")]
    [InlineData("//evil.example", "/")]
    [InlineData("/\\evil.example", "/")]
    [InlineData("https://evil.example/", "/")]
    [InlineData("/\r\nSet-Cookie: x=1", "/")]
    [InlineData("/caf€", "/")]
    [InlineData(null, "/")]
    public void ReturnToStaysOnThisSite(string? returnTo, string expected) => Assert.Equal(expected, OidcSignIn.SafeReturnTo(returnTo));

    [Theory]
    [InlineData("HTTPS://MCP.Example.com/Mcp", "https://mcp.example.com/Mcp")]
    [InlineData("http://127.0.0.1:8080/mcp", "http://127.0.0.1:8080/mcp")]
    public void AnAudienceComparesSchemeAndHostCaseInsensitively(string audience, string canonical) =>
        Assert.Equal(canonical, ResourceServer.Canonical(audience));
}

public class DatabaseUrls
{
    [Fact]
    public void APostgresUrlBecomesAnNpgsqlConnectionString()
    {
        var csb = new NpgsqlConnectionStringBuilder(Database.ConnectionString("postgresql://gw:p%40ss@db:5433/app"));
        Assert.Equal(("db", 5433, "app", "gw", "p@ss"), (csb.Host, csb.Port, csb.Database, csb.Username, csb.Password));
    }

    [Fact]
    public void SslmodeAndRootCertificateReachNpgsql()
    {
        var cs = new NpgsqlConnectionStringBuilder(Database.ConnectionString(
            "postgresql://u:p@db.example:5433/app?sslmode=verify-full&sslrootcert=/etc/ca.pem"));
        Assert.Equal(SslMode.VerifyFull, cs.SslMode);
        Assert.Equal("/etc/ca.pem", cs.RootCertificate);
        Assert.Throws<ArgumentException>(() => Database.ConnectionString("postgresql://u@h/d?sslmode=verify"));
        Assert.Throws<ArgumentException>(() => Database.ConnectionString("postgresql://u@h/d?target_session_attrs=any"));
    }

    [Theory]
    [InlineData("postgresql://u@h/d")]
    [InlineData("postgresql://u@h/d?connect_timeout=60")]
    public void ConnectAndAcquireWaitFiveSeconds(string url)
    {
        using var source = Database.CreateDataSource(url);
        Assert.Equal(5, new NpgsqlConnectionStringBuilder(source.ConnectionString).Timeout);
    }
}

public class RateLimits
{
    private sealed class ManualClock : TimeProvider
    {
        public long Ticks;
        public override long TimestampFrequency => TimeSpan.TicksPerSecond;
        public override long GetTimestamp() => Ticks;
        public void Advance(TimeSpan by) => Ticks += by.Ticks;
    }

    [Fact]
    public void ABucketEmptiesAndRefillsContinuously()
    {
        var clock = new ManualClock();
        var buckets = new TokenBuckets(60, clock);
        for (var i = 0; i < 60; i++)
        {
            Assert.True(buckets.TryTake("a").Allowed);
        }
        Assert.Equal((false, 1), buckets.TryTake("a"));
        Assert.True(buckets.TryTake("b").Allowed);
        clock.Advance(TimeSpan.FromSeconds(1));
        Assert.True(buckets.TryTake("a").Allowed);
        Assert.False(buckets.TryTake("a").Allowed);
    }

    [Fact]
    public void ZeroTurnsALimitOff() => Assert.True(new TokenBuckets(0, TimeProvider.System).TryTake("a").Allowed);

    [Fact]
    public void TheLeastRecentlySeenKeyGoesFirst()
    {
        var buckets = new TokenBuckets(1, new ManualClock(), maxKeys: 2);
        buckets.TryTake("a");
        buckets.TryTake("b");
        buckets.TryTake("a");
        buckets.TryTake("c");
        Assert.False(buckets.TryTake("a").Allowed);
        Assert.True(buckets.TryTake("b").Allowed);
    }
}

public class ChannelDecorators
{
    private sealed class FailingChannel(Func<int, Exception?> failure) : IDbChannel
    {
        public int Calls;

        private T Answer<T>(T value) => failure(++Calls) is { } e ? throw e : value;

        public Task<Policy> RestPolicyAsync(RequestContext ctx, string? requestedIsolation, CancellationToken ct) =>
            Task.FromResult(Answer(new Policy()));

        public Task<RestResult> RestInvokeAsync(RequestContext ctx, Policy policy, IReadOnlyDictionary<string, string> headers,
                                                byte[]? body, CancellationToken ct) =>
            Task.FromResult(Answer(new RestResult(200, new Dictionary<string, string>(), [])));

        public Task<Policy> McpPolicyAsync(RequestContext ctx, string request, string? requestedIsolation, McpHeaders headers,
                                           CancellationToken ct) =>
            Task.FromResult(Answer(new Policy()));

        public Task<string?> McpRequestAsync(RequestContext ctx, Policy policy, string request, string? callerContext,
                                             CancellationToken ct) =>
            Task.FromResult<string?>(Answer("{}"));
    }

    private static readonly RequestContext Ctx = RequestContext.New("rest", "GET", "/x");
    private static readonly Dictionary<string, string> NoHeaders = new();
    private static Exception Dropped() => new NpgsqlException("broken", new IOException("reset"));

    private static ConnectionRetry Retry(IDbChannel inner) => new(inner, NullLogger<ConnectionRetry>.Instance, baseDelaySeconds: 0);

    [Fact]
    public async Task AWriteIsNeverSentAgainAfterTheConnectionDropped()
    {
        var inner = new FailingChannel(n => n == 1 ? Dropped() : null);
        await Assert.ThrowsAsync<NpgsqlException>(() => Retry(inner).RestInvokeAsync(Ctx, new Policy(), NoHeaders, null, default));
        Assert.Equal(1, inner.Calls);

        inner = new FailingChannel(n => n == 1 ? Dropped() : null);
        await Assert.ThrowsAsync<NpgsqlException>(() => Retry(inner).McpRequestAsync(Ctx, new Policy(), "{}", null, default));
        Assert.Equal(1, inner.Calls);
    }

    [Fact]
    public async Task APolicyLookupAndAReadOnlyDispatchAreSentAgain()
    {
        var inner = new FailingChannel(n => n == 1 ? Dropped() : null);
        await Retry(inner).RestPolicyAsync(Ctx, null, default);
        Assert.Equal(2, inner.Calls);

        inner = new FailingChannel(n => n == 1 ? Dropped() : null);
        await Retry(inner).RestInvokeAsync(Ctx, new Policy(ReadOnly: true), NoHeaders, null, default);
        Assert.Equal(2, inner.Calls);
    }

    [Fact]
    public async Task AServerAnswerOrAnExhaustedPoolIsNoDroppedConnection()
    {
        foreach (var failure in new Exception[]
                 {
                     new PostgresException("conflict", "ERROR", "ERROR", PostgresErrorCodes.SerializationFailure),
                     new DatabaseUnavailableException(new NpgsqlException("pool", new TimeoutException())),
                 })
        {
            var inner = new FailingChannel(n => n == 1 ? failure : null);
            await Assert.ThrowsAnyAsync<Exception>(() => Retry(inner).RestPolicyAsync(Ctx, null, default));
            Assert.Equal(1, inner.Calls);
        }
        Assert.True(ConnectionRetry.ConnectionLost(new NpgsqlException("x", new SocketException())));
        Assert.True(ConnectionRetry.ConnectionLost(new PostgresException("shutdown", "FATAL", "FATAL", "57P01")));
    }

    [Fact]
    public async Task ASerializationFailureRunsTheWholeTransactionAgainUntilAttemptsRunOut()
    {
        var conflict = new PostgresException("conflict", "ERROR", "ERROR", PostgresErrorCodes.SerializationFailure);
        var inner = new FailingChannel(n => n == 1 ? conflict : null);
        await new SerializationRetry(inner, 3, 0).RestInvokeAsync(Ctx, new Policy(), NoHeaders, null, default);
        Assert.Equal(2, inner.Calls);

        inner = new FailingChannel(_ => conflict);
        var exhausted = await Assert.ThrowsAsync<RetriesExhaustedException>(
            () => new SerializationRetry(inner, 3, 0).McpRequestAsync(Ctx, new Policy(), "{}", null, default));
        Assert.Equal((3, PostgresErrorCodes.SerializationFailure, 3), (exhausted.Attempts, exhausted.SqlState, inner.Calls));
    }
}

public class Interceptors
{
    private sealed class Probe(FailurePolicy failure, Func<InterceptedRequest, Task> body, bool needsJson = true) : IRequestInterceptor
    {
        public string Name => "probe";
        public bool NeedsJson => needsJson;
        public FailurePolicy Failure => failure;
        public TimeSpan Timeout => TimeSpan.FromMilliseconds(200);
        public bool AppliesTo(RequestContext ctx) => true;
        public async ValueTask OnRequestAsync(InterceptedRequest request, RequestContext ctx, CancellationToken ct) => await body(request);
    }

    private static readonly byte[] Odd = Encoding.UTF8.GetBytes("{ \"text\" :\"h\\u00e9llo\",\n \"n\": 1.50 }");

    private static async Task<InterceptedRequest> Run(params IRequestInterceptor[] interceptors)
    {
        var pipeline = new InterceptorPipeline(interceptors, [], NullLogger<InterceptorPipeline>.Instance);
        var request = new InterceptedRequest("POST", "/x", new Dictionary<string, string>(), Odd);
        var ctx = RequestContext.New("rest", "POST", "/x");
        await pipeline.RunAsync(pipeline.RequestsFor(ctx), request, ctx, CancellationToken.None);
        return request;
    }

    [Fact]
    public async Task AHeaderOnlyInterceptorLeavesTheBytesAlone()
    {
        var request = await Run(new Probe(FailurePolicy.Fail, r => { r.Headers["x-a"] = "1"; return Task.CompletedTask; }, needsJson: false));
        Assert.Same(Odd, request.Body);
        Assert.Equal("1", request.Headers["x-a"]);
    }

    [Fact]
    public async Task JsonIsParsedOnceAndSerializedOnce()
    {
        var request = await Run(
            new Probe(FailurePolicy.Fail, r => { r.Json!["a"] = 1; return Task.CompletedTask; }),
            new Probe(FailurePolicy.Fail, r => { r.Json!["b"] = 2; return Task.CompletedTask; }));
        Assert.Equal("{\"text\":\"héllo\",\"n\":1.50,\"a\":1,\"b\":2}", Encoding.UTF8.GetString(request.Body!));
    }

    [Fact]
    public async Task DegradeGoesOnAsBeforeAndFailStops()
    {
        Task Broken(InterceptedRequest r)
        {
            r.Json!["half"] = true;
            throw new InvalidOperationException("down");
        }
        var request = await Run(new Probe(FailurePolicy.Degrade, Broken));
        Assert.Equal(Odd, request.Body);
        await Assert.ThrowsAsync<InterceptorFailedException>(() => Run(new Probe(FailurePolicy.Fail, Broken)));
    }

    [Fact]
    public async Task OverrunningTheTimeoutIsAFailure() =>
        await Assert.ThrowsAsync<InterceptorFailedException>(() => Run(new Probe(FailurePolicy.Fail, _ => Task.Delay(Timeout.Infinite))));

    [Fact]
    public async Task TheFakeEmbedderIsTheSameInEveryLanguage()
    {
        var vectors = await new FakeEmbedder().EmbedAsync(["hello", "héllo wörld"], default);
        Assert.Equal([0.1725, 0.949, 0.302, 0.7294], vectors[0]);
        Assert.Equal([0.6314, 0.0, 0.2471, 0.4902], vectors[1]);
        await Assert.ThrowsAsync<ArgumentException>(() => new FakeEmbedder().EmbedAsync([new string('x', 2001)], default));
    }

    [Fact]
    public async Task EmbedFillsTheMarkedFieldsAndKeepsAVectorTheClientSent()
    {
        using var annotations = JsonDocument.Parse("""{"text": {"x-embed": true}, "kept": {"x-embed": true}, "other": {}}""");
        var ctx = RequestContext.New("mcp", "tools/call", "/mcp") with
        {
            Annotations = annotations.RootElement.EnumerateObject().ToDictionary(p => p.Name, p => p.Value.Clone()),
        };
        var embed = new EmbedFields(new FakeEmbedder(), Options.Create(new GatewayOptions()));
        Assert.True(embed.AppliesTo(ctx));
        var request = new InterceptedRequest("POST", "/mcp", new Dictionary<string, string>(), Encoding.UTF8.GetBytes(
            """{"params": {"arguments": {"text": "hello", "kept": "x", "kept_embedding": [1], "other": "y"}}}"""));
        await embed.OnRequestAsync(request, ctx, default);
        var args = request.Json!["params"]!["arguments"]!;
        Assert.Equal("[0.1725,0.949,0.302,0.7294]", args["text_embedding"]!.ToJsonString());
        Assert.Equal("[1]", args["kept_embedding"]!.ToJsonString());
        Assert.Null(args["other_embedding"]);
    }
}

public class McpRules
{
    [Theory]
    [InlineData("""{"error": {"code": -32601}}""", true, 404)]
    [InlineData("""{"error": {"code": -32022}}""", true, 400)]
    [InlineData("""{"error": {"code": -32602}}""", true, 200)]
    [InlineData("""{"error": {"code": -32601}}""", false, 200)]
    [InlineData("""{"result": {}}""", true, 200)]
    public void TheStatusOfADispatchedAnswer(string envelope, bool modern, int status) =>
        Assert.Equal(status, McpEndpoint.EnvelopeStatus(envelope, modern));

    [Theory]
    [InlineData("Bearer cn_abc", "cn_abc")]
    [InlineData("bearer  cn_abc ", "cn_abc")]
    [InlineData("Bearer eyJhbGciOi", null)]
    [InlineData("Basic cn_abc", null)]
    [InlineData("", null)]
    public void ABearerConnectionHandle(string authorization, string? handle) =>
        Assert.Equal(handle, McpEndpoint.BearerHandle(authorization));

    [Theory]
    [InlineData("""{"error": {"code": -31001, "data": {"code": "pgmi.connection_unknown"}}}""", true)]
    [InlineData("""{"error": {"code": -31001, "data": {"code": "membership.connection.paused"}}}""", false)]
    [InlineData("""{"result": {}}""", false)]
    public void AnUnknownHandleIsUnauthorized(string envelope, bool unknown) =>
        Assert.Equal(unknown, McpEndpoint.HandleUnknown(envelope));

    [Theory]
    [InlineData("application/json", true)]
    [InlineData("text/event-stream, application/json", true)]
    [InlineData("*/*", true)]
    [InlineData("application/json;q=0", false)]
    [InlineData("text/html", false)]
    public void AcceptAdmitsJson(string accept, bool expected) => Assert.Equal(expected, McpEndpoint.AcceptsJson(accept));
}
