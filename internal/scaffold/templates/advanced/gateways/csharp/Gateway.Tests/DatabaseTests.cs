// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own tests that fit this project.
//
// Against a deployed template: an env=dev deploy and DATABASE_URL as a login
// in the api role (../README.md). Without DATABASE_URL these return early and
// say so (xunit 2 has no runtime skip). pgmi's end-to-end harness
// (internal/scaffold/testdata/gateways/e2e) checks the full wire contract;
// these check the same gateway from inside, the way a project's CI would.

using System.Net;
using System.Net.Http.Json;
using System.Security.Cryptography;
using System.Text.Json;
using Microsoft.AspNetCore.Mvc.Testing;
using Microsoft.Extensions.DependencyInjection;
using Npgsql;
using Xunit;
using Xunit.Abstractions;

namespace Pgmi.Gateway.Tests;

public sealed class SignedInGateway : IAsyncLifetime
{
    public static readonly string? DatabaseUrl = Environment.GetEnvironmentVariable("DATABASE_URL");

    public GatewayFactory Factory { get; } = new(new() { ["DEV_LOGIN"] = "1" });
    public HttpClient Http { get; private set; } = null!;
    public string Session { get; private set; } = "";
    public string Csrf { get; private set; } = "";

    public async Task InitializeAsync()
    {
        Http = Factory.CreateClient(new WebApplicationFactoryClientOptions { HandleCookies = false });
        if (DatabaseUrl is not null)
        {
            (Session, Csrf) = await SignIn("client-csharp");
        }
    }

    // Development sign-in. In production the gateway runs OIDC and calls
    // api.open_login_session instead (SignIn.cs).
    public async Task<(string Session, string Csrf)> SignIn(string subject)
    {
        var signIn = await Http.PostAsJsonAsync("/auth/dev-login", new { subject, email = subject + "@example.com", displayName = subject });
        Assert.Equal(HttpStatusCode.NoContent, signIn.StatusCode);
        var cookies = signIn.Headers.GetValues("Set-Cookie").Select(c => c.Split(';')[0].Split('=', 2)).ToDictionary(c => c[0], c => c[1]);
        return (cookies[SessionCookies.Session], cookies[SessionCookies.Csrf]);
    }

    public Task DisposeAsync()
    {
        Factory.Dispose();
        return Task.CompletedTask;
    }
}

public class DatabaseTests(SignedInGateway gateway, ITestOutputHelper output) : IClassFixture<SignedInGateway>
{
    private bool Skipped()
    {
        if (SignedInGateway.DatabaseUrl is null)
        {
            output.WriteLine("skipped: DATABASE_URL is not set");
            return true;
        }
        return false;
    }

    private async Task<(HttpStatusCode Status, JsonElement Body)> Call(HttpMethod method, string path, object? body = null,
                                                                      Dictionary<string, string>? headers = null, bool signedIn = false)
    {
        using var request = new HttpRequestMessage(method, path);
        request.Headers.Add("Accept", "application/json");
        if (body is not null)
        {
            request.Content = JsonContent.Create(body);
        }
        if (signedIn)
        {
            request.Headers.Add("Cookie", $"{SessionCookies.Session}={gateway.Session}");
        }
        foreach (var (name, value) in headers ?? [])
        {
            request.Headers.TryAddWithoutValidation(name, value);
        }
        using var response = await gateway.Http.SendAsync(request);
        var text = await response.Content.ReadAsStringAsync();
        return (response.StatusCode, text.Length > 0 ? JsonDocument.Parse(text).RootElement.Clone() : default);
    }

    private Task<(HttpStatusCode Status, JsonElement Body)> Post(string path, object body, Dictionary<string, string>? headers = null) =>
        Call(HttpMethod.Post, path, body, new Dictionary<string, string>(headers ?? []) { ["X-CSRF-Token"] = gateway.Csrf }, signedIn: true);

    private static void AssertCode((HttpStatusCode Status, JsonElement Body) response, int status, string code)
    {
        Assert.Equal(status, (int)response.Status);
        Assert.Equal(code, response.Body.GetProperty("code").GetString());
    }

    [Fact]
    public async Task OpenApiListsWhatTheCallerMayCall()
    {
        if (Skipped()) return;
        var anonymous = await Call(HttpMethod.Get, "/api/openapi.json");
        Assert.True(anonymous.Body.GetProperty("paths").TryGetProperty("/hello", out _));
        Assert.False(anonymous.Body.GetProperty("paths").TryGetProperty("/me", out _));
        var signedIn = await Call(HttpMethod.Get, "/api/openapi.json", signedIn: true);
        Assert.True(signedIn.Body.GetProperty("paths").TryGetProperty("/me", out _));
    }

    [Fact]
    public async Task APublicAndAnAuthenticatedRoute()
    {
        if (Skipped()) return;
        Assert.Equal("Hello, CSharp!", (await Call(HttpMethod.Get, "/api/hello?name=CSharp")).Body.GetProperty("message").GetString());
        Assert.Equal(HttpStatusCode.Unauthorized, (await Call(HttpMethod.Get, "/api/me")).Status);
        Assert.Equal(HttpStatusCode.OK, (await Call(HttpMethod.Get, "/api/me", signedIn: true)).Status);
        Assert.Equal(HttpStatusCode.Unauthorized,
                     (await Call(HttpMethod.Get, "/api/me", headers: new() { ["X-Login-Session"] = gateway.Session })).Status);
    }

    [Fact]
    public async Task CodedProblems()
    {
        if (Skipped()) return;
        AssertCode(await Call(HttpMethod.Post, "/api/connections", new { clientName = "x" }, signedIn: true), 403, "pgmi.csrf_refused");
        AssertCode(await Post("/api/connections", new { clientName = 5 }), 400, "pgmi.invalid_body");
        AssertCode(await Post("/api/connections", new { clientName = "x", organizationId = "00000000-0000-4000-8000-000000000000", roleId = "agent.reader" }),
                   422, "membership.connection.not_member");
        AssertCode(await Call(HttpMethod.Get, "/api/hello", headers: new() { ["X-PGMI-Transaction-Isolation"] = "snapshot" }),
                   400, "pgmi.transaction_isolation_unsupported");

        var created = await Post("/api/organizations", new { name = "C# client", slug = "cs-" + Convert.ToHexStringLower(RandomNumberGenerator.GetBytes(4)) });
        Assert.Equal(HttpStatusCode.Created, created.Status);
        var organization = created.Body.GetProperty("id").GetString();
        var opened = await Post("/api/organization-sessions", new { organizationId = organization });
        Assert.Equal(HttpStatusCode.Created, opened.Status);
        AssertCode(await Post("/api/connections", new { clientName = "x", validDays = 999, organizationId = organization, roleId = "agent.reader" },
                              new() { ["x-organization-session"] = opened.Body.GetProperty("organizationSession").GetString()! }),
                   422, "membership.connection.validity_invalid");
    }

    [Fact]
    public async Task HeadAnswersTheGetLengthWithoutContent()
    {
        if (Skipped()) return;
        var length = (await gateway.Http.GetByteArrayAsync("/api/openapi.json")).Length;
        using var head = await gateway.Http.SendAsync(new HttpRequestMessage(HttpMethod.Head, "/api/openapi.json"));
        Assert.Equal(HttpStatusCode.OK, head.StatusCode);
        Assert.Equal(length, head.Content.Headers.ContentLength);
        Assert.Empty(await head.Content.ReadAsByteArrayAsync());
    }

    [Fact]
    public async Task LogoutClosesTheSessionAndClearsBothCookies()
    {
        if (Skipped()) return;
        var (session, csrf) = await gateway.SignIn("client-csharp-logout");
        async Task<HttpResponseMessage> Send(HttpMethod method, string path, string? token = null)
        {
            var request = new HttpRequestMessage(method, path);
            request.Headers.Add("Cookie", $"{SessionCookies.Session}={session}; {SessionCookies.Csrf}={csrf}");
            if (token is not null)
            {
                request.Headers.Add("X-CSRF-Token", token);
            }
            return await gateway.Http.SendAsync(request);
        }

        Assert.Equal(HttpStatusCode.OK, (await Send(HttpMethod.Get, "/api/me")).StatusCode);
        Assert.Equal(HttpStatusCode.Forbidden, (await Send(HttpMethod.Post, "/auth/logout")).StatusCode);
        var logout = await Send(HttpMethod.Post, "/auth/logout", csrf);
        Assert.Equal(HttpStatusCode.NoContent, logout.StatusCode);
        var cleared = logout.Headers.GetValues("Set-Cookie").ToList();
        Assert.Contains(cleared, c => c.StartsWith("__Host-session=;") && c.Contains("max-age=0") && c.Contains("httponly"));
        Assert.Contains(cleared, c => c.StartsWith("__Host-csrf=;") && c.Contains("max-age=0") && !c.Contains("httponly"));
        Assert.Equal(HttpStatusCode.Unauthorized, (await Send(HttpMethod.Get, "/api/me")).StatusCode);
    }

    [Fact]
    public async Task McpListsToolsForAnonymousCallers()
    {
        if (Skipped()) return;
        var request = HttpTests.Mcp("""{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}""");
        request.Headers.Add("MCP-Protocol-Version", "2025-11-25");
        using var response = await gateway.Http.SendAsync(request);
        Assert.Equal(HttpStatusCode.OK, response.StatusCode);
        Assert.Equal("2025-11-25", response.Headers.GetValues("MCP-Protocol-Version").Single());
        var envelope = JsonDocument.Parse(await response.Content.ReadAsStringAsync()).RootElement;
        Assert.Equal(1, envelope.GetProperty("id").GetInt32());
        Assert.Equal(JsonValueKind.Array, envelope.GetProperty("result").GetProperty("tools").ValueKind);
    }

    [Theory]
    [InlineData("X-Connection", "cn_unknown")]
    [InlineData("Authorization", "Bearer cn_unknown")]
    public async Task AnUnknownConnectionHandleIsUnauthorized(string header, string value)
    {
        if (Skipped()) return;
        var request = HttpTests.Mcp("""{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}""");
        request.Headers.Add("MCP-Protocol-Version", "2025-11-25");
        request.Headers.TryAddWithoutValidation(header, value);
        using var response = await gateway.Http.SendAsync(request);
        Assert.Equal(HttpStatusCode.Unauthorized, response.StatusCode);
        Assert.Equal("Bearer error=\"invalid_token\"", response.Headers.WwwAuthenticate.ToString());
    }

    [Fact]
    public async Task AnExhaustedPoolIsUnavailableAfterFiveSeconds()
    {
        if (Skipped()) return;
        await using var source = Database.CreateDataSource(SignedInGateway.DatabaseUrl!, maxPoolSize: 1);
        await using var held = await source.OpenConnectionAsync();
        var clock = System.Diagnostics.Stopwatch.StartNew();
        await Assert.ThrowsAsync<DatabaseUnavailableException>(
            () => new PostgresChannel(source).RestPolicyAsync(RequestContext.New("rest", "GET", "/hello"), null, CancellationToken.None));
        Assert.InRange(clock.Elapsed.TotalSeconds, 4, 8);
    }

    [Fact]
    public async Task ApplicationNameAndABoundedPool()
    {
        if (Skipped()) return;
        await Call(HttpMethod.Get, "/api/hello");
        var dataSource = gateway.Factory.Services.GetRequiredService<NpgsqlDataSource>();
        await using var command = dataSource.CreateCommand(
            "SELECT count(*)::int FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1");
        command.Parameters.Add(new() { Value = Database.ApplicationName });
        Assert.InRange((int)(await command.ExecuteScalarAsync())!, 1, 10);
    }
}

// TLS_DATABASE_URL names a TLS-only server (testdata/gateways/tls-postgres.sh)
// with sslmode=require; TLS_CA and TLS_WRONG_CA are its CA and an unrelated one.
// Without them the test returns early and says so; CI sets REQUIRE_TLS_TEST.
public class TlsTests(ITestOutputHelper output)
{
    private static async Task<bool> Encrypted(string url)
    {
        await using var source = Database.CreateDataSource(url);
        await using var connection = await source.OpenConnectionAsync();
        await using var command = new NpgsqlCommand("SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()", connection);
        return (bool)(await command.ExecuteScalarAsync())!;
    }

    [Fact]
    public async Task TlsFollowsLibpqSslmode()
    {
        var url = Environment.GetEnvironmentVariable("TLS_DATABASE_URL");
        if (string.IsNullOrEmpty(url))
        {
            Assert.Null(Environment.GetEnvironmentVariable("REQUIRE_TLS_TEST"));
            output.WriteLine("skipped: TLS_DATABASE_URL is not set");
            return;
        }
        var verify = url.Replace("sslmode=require", "sslmode=verify-full");
        Assert.True(await Encrypted(url));
        Assert.True(await Encrypted(verify + "&sslrootcert=" + Uri.EscapeDataString(Environment.GetEnvironmentVariable("TLS_CA")!)));
        await Assert.ThrowsAnyAsync<Exception>(
            () => Encrypted(verify + "&sslrootcert=" + Uri.EscapeDataString(Environment.GetEnvironmentVariable("TLS_WRONG_CA")!)));
    }
}
