// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own tests that fit this project.
//
// The gateway in memory (WebApplicationFactory), configured per test the way
// the environment configures it in production. These tests need no database:
// every answer here is given before one is called. DatabaseTests.cs needs one.

using System.Net;
using System.Net.Http.Json;
using System.Text;
using System.Text.Json;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Mvc.Testing;
using Microsoft.AspNetCore.TestHost;
using Microsoft.Extensions.DependencyInjection;
using Xunit;

namespace Pgmi.Gateway.Tests;

// The gateway with these settings on top of the environment. Unset, it
// points at a database nobody listens on.
public sealed class GatewayFactory(Dictionary<string, string?> settings) : WebApplicationFactory<Program>
{
    public const string NoDatabase = "postgresql://nobody@127.0.0.1:9/none";

    protected override void ConfigureWebHost(IWebHostBuilder builder)
    {
        builder.UseSetting("DATABASE_URL", Environment.GetEnvironmentVariable("DATABASE_URL") ?? NoDatabase);
        foreach (var (key, value) in settings)
        {
            builder.UseSetting(key, value);
        }
    }
}

public sealed class DefaultGateway : IDisposable
{
    public GatewayFactory Factory { get; } = new([]);

    public void Dispose() => Factory.Dispose();
}

public class HttpTests(DefaultGateway gateway) : IClassFixture<DefaultGateway>
{
    private readonly HttpClient _http = gateway.Factory.CreateClient();

    internal static async Task<JsonElement> AssertProblem(HttpResponseMessage response, HttpStatusCode status, string code)
    {
        Assert.Equal(status, response.StatusCode);
        Assert.Equal("application/problem+json", response.Content.Headers.ContentType?.MediaType);
        var body = JsonDocument.Parse(await response.Content.ReadAsStringAsync()).RootElement;
        Assert.Equal(code, body.GetProperty("code").GetString());
        return body;
    }

    internal static HttpRequestMessage Mcp(string body, string contentType = "application/json")
    {
        var request = new HttpRequestMessage(HttpMethod.Post, "/mcp") { Content = new StringContent(body, Encoding.UTF8) };
        request.Content.Headers.ContentType = new(contentType);
        return request;
    }

    [Fact]
    public async Task Health()
    {
        var body = await _http.GetFromJsonAsync<JsonElement>("/health");
        Assert.Equal("healthy", body.GetProperty("status").GetString());
    }

    [Theory]
    [InlineData("GET", "/nowhere")]
    [InlineData("POST", "/nowhere")]
    [InlineData("DELETE", "/auth/nowhere")]
    [InlineData("GET", "/API/hello")]
    [InlineData("GET", "/apix")]
    [InlineData("GET", "/.well-known/oauth-protected-resource")]
    public async Task AnUnknownPathIsNotFound(string method, string path)
    {
        var body = await AssertProblem(await _http.SendAsync(new HttpRequestMessage(new HttpMethod(method), path)),
                                       HttpStatusCode.NotFound, "pgmi.not_found");
        Assert.Equal("about:blank", body.GetProperty("type").GetString());
    }

    [Theory]
    [InlineData("GET")]
    [InlineData("DELETE")]
    [InlineData("PUT")]
    public async Task McpTakesOnlyPost(string method)
    {
        var response = await _http.SendAsync(new HttpRequestMessage(new HttpMethod(method), "/mcp"));
        await AssertProblem(response, HttpStatusCode.MethodNotAllowed, "pgmi.method_not_allowed");
        Assert.Equal("POST", string.Join(",", response.Content.Headers.Allow));
    }

    [Fact]
    public async Task McpRefusesWhatItCannotAnswerBeforeReadingTheBody()
    {
        await AssertProblem(await _http.SendAsync(Mcp("{}", "text/plain")), HttpStatusCode.UnsupportedMediaType, "pgmi.unsupported_media_type");
        var accept = Mcp("{}");
        accept.Headers.Add("Accept", "text/event-stream");
        await AssertProblem(await _http.SendAsync(accept), HttpStatusCode.NotAcceptable, "pgmi.not_acceptable");
        var origin = Mcp("{}");
        origin.Headers.Add("Origin", "http://evil.example");
        await AssertProblem(await _http.SendAsync(origin), HttpStatusCode.Forbidden, "pgmi.origin_refused");
    }

    [Theory]
    [InlineData("not-json")]
    [InlineData("")]
    public async Task AnUnparsableMcpBodyIsAParseError(string body)
    {
        var response = await _http.SendAsync(Mcp(body));
        Assert.Equal(HttpStatusCode.BadRequest, response.StatusCode);
        var envelope = JsonDocument.Parse(await response.Content.ReadAsStringAsync()).RootElement;
        Assert.Equal(-32700, envelope.GetProperty("error").GetProperty("code").GetInt32());
        Assert.Equal(JsonValueKind.Null, envelope.GetProperty("id").ValueKind);
    }

    [Fact]
    public async Task ADisallowedOriginIsRefusedOnRest() =>
        await AssertProblem(await _http.SendAsync(new HttpRequestMessage(HttpMethod.Get, "/api/hello") { Headers = { { "Origin", "http://evil.example" } } }),
                            HttpStatusCode.Forbidden, "pgmi.origin_refused");

    [Fact]
    public async Task WithoutTheDatabaseRestIs503()
    {
        if (Environment.GetEnvironmentVariable("DATABASE_URL") is not null)
        {
            return;
        }
        var response = await _http.GetAsync("/api/hello");
        await AssertProblem(response, HttpStatusCode.ServiceUnavailable, "pgmi.unavailable");
        Assert.Equal("1", response.Headers.RetryAfter?.ToString());
    }
}

// Whatever the channel was doing when the deadline passed, the answer is a
// 504 in the protocol's own shape.
public class DeadlineAnswers(DefaultGateway gateway) : IClassFixture<DefaultGateway>
{
    private sealed class ExpiredChannel : IDbChannel
    {
        public Task<Policy> RestPolicyAsync(RequestContext ctx, string? requestedIsolation, CancellationToken ct) =>
            Task.FromResult(new Policy());

        public Task<RestResult> RestInvokeAsync(RequestContext ctx, Policy policy, IReadOnlyDictionary<string, string> headers,
                                                byte[]? body, CancellationToken ct) =>
            throw new DeadlineExceededException(null);

        public Task<Policy> McpPolicyAsync(RequestContext ctx, string request, string? requestedIsolation, McpHeaders headers,
                                           CancellationToken ct) =>
            Task.FromResult(new Policy());

        public Task<string?> McpRequestAsync(RequestContext ctx, Policy policy, string request, string? callerContext,
                                             CancellationToken ct) =>
            throw new DeadlineExceededException(null);
    }

    private readonly HttpClient _http = gateway.Factory
        .WithWebHostBuilder(b => b.ConfigureTestServices(s => s.AddSingleton<IDbChannel>(new ExpiredChannel())))
        .CreateClient();

    [Fact]
    public async Task RestIsAProblem()
    {
        var response = await _http.GetAsync("/api/hello");
        await HttpTests.AssertProblem(response, HttpStatusCode.GatewayTimeout, "pgmi.deadline_exceeded");
        Assert.Single(response.Headers.GetValues("X-Request-Id"));
    }

    [Fact]
    public async Task McpIsAnInternalErrorWithTheRequestId()
    {
        var response = await _http.SendAsync(HttpTests.Mcp("""{"jsonrpc": "2.0", "id": 7, "method": "tools/list"}"""));
        Assert.Equal(HttpStatusCode.GatewayTimeout, response.StatusCode);
        var envelope = JsonDocument.Parse(await response.Content.ReadAsStringAsync()).RootElement;
        Assert.Equal(7, envelope.GetProperty("id").GetInt32());
        var error = envelope.GetProperty("error");
        Assert.Equal(-32603, error.GetProperty("code").GetInt32());
        Assert.Equal("pgmi.deadline_exceeded", error.GetProperty("data").GetProperty("code").GetString());
        Assert.False(string.IsNullOrEmpty(error.GetProperty("data").GetProperty("requestId").GetString()));
    }
}

public class OAuthTests
{
    private static GatewayFactory Factory(params (string, string?)[] extra) =>
        new(new Dictionary<string, string?>
        {
            ["MCP_RESOURCE"] = "http://127.0.0.1:8080/mcp",
            ["MCP_AUTHORIZATION_SERVERS"] = "http://127.0.0.1:9",
        }.Concat(extra.Select(e => KeyValuePair.Create(e.Item1, e.Item2))).ToDictionary());

    [Fact]
    public async Task MetadataAndTheChallenge()
    {
        using var factory = Factory();
        var http = factory.CreateClient();
        foreach (var path in new[] { "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp" })
        {
            var metadata = await http.GetFromJsonAsync<JsonElement>(path);
            Assert.Equal("http://127.0.0.1:8080/mcp", metadata.GetProperty("resource").GetString());
            Assert.Equal("http://127.0.0.1:9", metadata.GetProperty("authorization_servers")[0].GetString());
        }
        var response = await http.SendAsync(HttpTests.Mcp("{}"));
        Assert.Equal(HttpStatusCode.Unauthorized, response.StatusCode);
        Assert.Equal("Bearer resource_metadata=\"http://127.0.0.1:8080/.well-known/oauth-protected-resource/mcp\", scope=\"openid\"",
                     response.Headers.WwwAuthenticate.ToString());
        Assert.Empty(await response.Content.ReadAsByteArrayAsync());
    }

    [Fact]
    public async Task ATokenThatIsNotAJwtIsRefusedWithoutAKeyFetch()
    {
        using var factory = Factory();
        var request = HttpTests.Mcp("{}");
        request.Headers.Add("Authorization", "Bearer not-a-jwt");
        var response = await factory.CreateClient().SendAsync(request);
        Assert.Equal(HttpStatusCode.Unauthorized, response.StatusCode);
        Assert.Contains("error=\"invalid_token\"", response.Headers.WwwAuthenticate.ToString());
    }

    [Fact]
    public async Task TheAddressLimitComesFirst()
    {
        using var factory = Factory(("RATE_LIMIT_PER_IP", "2"));
        var http = factory.CreateClient();
        for (var i = 0; i < 2; i++)
        {
            Assert.Equal(HttpStatusCode.Unauthorized, (await http.SendAsync(HttpTests.Mcp("{}"))).StatusCode);
        }
        var response = await http.SendAsync(HttpTests.Mcp("{}"));
        await HttpTests.AssertProblem(response, HttpStatusCode.TooManyRequests, "pgmi.rate_limited");
        Assert.True(int.Parse(response.Headers.GetValues("Retry-After").Single()) >= 1);
    }

    [Fact]
    public async Task SignInsShareTheAddressLimitWithMcp()
    {
        using var factory = Factory(("RATE_LIMIT_PER_IP", "3"));
        var http = factory.CreateClient();
        Assert.Equal(HttpStatusCode.Unauthorized, (await http.SendAsync(HttpTests.Mcp("{}"))).StatusCode);
        for (var i = 0; i < 2; i++)
        {
            Assert.Equal(HttpStatusCode.NotFound, (await http.GetAsync("/auth/login")).StatusCode);
        }
        foreach (var path in new[] { "/auth/login", "/auth/callback?code=x&state=y" })
        {
            var response = await http.GetAsync(path);
            await HttpTests.AssertProblem(response, HttpStatusCode.TooManyRequests, "pgmi.rate_limited");
            Assert.True(int.Parse(response.Headers.GetValues("Retry-After").Single()) >= 1);
        }
    }
}

public class StartupRefusals
{
    [Theory]
    [InlineData("INTERCEPTORS", "embed,nonsense")]
    [InlineData("EMBED_FAILURE_POLICY", "ignore")]
    [InlineData("MCP_RESOURCE", "http://mcp.example.com/mcp")]
    [InlineData("OIDC_ISSUER", "https://login.example.com")]
    [InlineData("REQUEST_DEADLINE_SECONDS", "0")]
    [InlineData("DATABASE_URL", "postgresql://u@h/d?sslmode=verify-ca")]
    public void ABadSettingStopsTheGatewayAtStartup(string key, string value)
    {
        using var factory = new GatewayFactory(new() { [key] = value, ["MCP_AUTHORIZATION_SERVERS"] = "https://login.example.com" });
        Assert.ThrowsAny<Exception>(() => factory.CreateClient());
    }
}
