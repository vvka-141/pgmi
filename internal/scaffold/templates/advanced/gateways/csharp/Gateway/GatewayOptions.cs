// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// Every setting, bound from configuration. The keys are the environment
// variables every reference gateway reads (../../PROTOCOL.md,
// "Configuration"), so one harness and one deployment recipe fit them all.
// ADAPT: bind a section of appsettings.json instead, or add your own keys.

using System.Net;
using Microsoft.Extensions.Options;

namespace Pgmi.Gateway;

public sealed class GatewayOptions
{
    [ConfigurationKeyName("DATABASE_URL")] public string DatabaseUrl { get; set; } = "";
    [ConfigurationKeyName("HOST")] public string Host { get; set; } = "127.0.0.1";
    [ConfigurationKeyName("PORT")] public int Port { get; set; } = 8080;
    [ConfigurationKeyName("ALLOWED_ORIGINS")] public string? AllowedOrigins { get; set; }
    [ConfigurationKeyName("LOGIN_CSRF_SECRET")] public string? CsrfSecret { get; set; }
    [ConfigurationKeyName("DEV_LOGIN")] public string? DevLoginSetting { get; set; }
    [ConfigurationKeyName("POOL_MAX_SIZE")] public int PoolMaxSize { get; set; } = 10;
    [ConfigurationKeyName("RETRY_MAX_ATTEMPTS")] public int RetryMaxAttempts { get; set; } = 3;
    [ConfigurationKeyName("RETRY_BASE_DELAY")] public double RetryBaseDelaySeconds { get; set; } = 0.05;
    [ConfigurationKeyName("INTERCEPTORS")] public string? Interceptors { get; set; }
    [ConfigurationKeyName("EMBED_FAILURE_POLICY")] public string EmbedFailurePolicy { get; set; } = "fail";
    [ConfigurationKeyName("OIDC_ISSUER")] public string? OidcIssuer { get; set; }
    [ConfigurationKeyName("OIDC_CLIENT_ID")] public string? OidcClientId { get; set; }
    [ConfigurationKeyName("OIDC_CLIENT_SECRET")] public string? OidcClientSecret { get; set; }
    [ConfigurationKeyName("OIDC_REDIRECT_URI")] public string? OidcRedirectUri { get; set; }
    [ConfigurationKeyName("OIDC_SCOPES")] public string OidcScopes { get; set; } = "openid email profile";
    [ConfigurationKeyName("MCP_RESOURCE")] public string? McpResource { get; set; }
    [ConfigurationKeyName("MCP_AUTHORIZATION_SERVERS")] public string? McpAuthorizationServers { get; set; }
    [ConfigurationKeyName("MCP_SCOPES")] public string McpScopes { get; set; } = "openid";
    [ConfigurationKeyName("RATE_LIMIT_PER_IP")] public int RateLimitPerIp { get; set; } = 600;
    [ConfigurationKeyName("RATE_LIMIT_PER_CLIENT")] public int RateLimitPerClient { get; set; } = 300;

    // Fixed here rather than configured. ADAPT: the limits your clients need.
    public const long MaxBodyBytes = 1024 * 1024;
    public static readonly TimeSpan PoolTimeout = TimeSpan.FromSeconds(5);
    public static readonly TimeSpan RequestDeadline = TimeSpan.FromSeconds(30);

    public bool DevLogin => DevLoginSetting == "1";
    public bool SignInEnabled => DevLogin || OidcIssuer is { Length: > 0 };

    public IReadOnlySet<string> Origins => Split(AllowedOrigins, ',') is { Count: > 0 } configured
        ? configured.ToHashSet()
        : new HashSet<string> { $"http://localhost:{Port}", $"http://127.0.0.1:{Port}" };

    public IReadOnlyList<string> InterceptorNames => Split(Interceptors, ',');
    public IReadOnlyList<string> AuthorizationServers => Split(McpAuthorizationServers, ',');

    public static IReadOnlyList<string> Split(string? value, char separator) =>
        (value ?? "").Split(separator, StringSplitOptions.TrimEntries | StringSplitOptions.RemoveEmptyEntries);

    // https, or http on a loopback host only: a test issuer on 127.0.0.1 is
    // fine, a plaintext production issuer is not.
    public static bool IsSecureOrLoopback(string url) =>
        Uri.TryCreate(url, UriKind.Absolute, out var uri)
        && (uri.Scheme == Uri.UriSchemeHttps
            || (uri.Scheme == Uri.UriSchemeHttp
                && (uri.IsLoopback || (IPAddress.TryParse(uri.Host.Trim('[', ']'), out var ip) && IPAddress.IsLoopback(ip)))));
}

// Refuses a configuration at startup (ValidateOnStart), so a typo is an exit
// with a message, never a gateway that runs half configured.
public sealed class GatewayOptionsValidator : IValidateOptions<GatewayOptions>
{
    public ValidateOptionsResult Validate(string? name, GatewayOptions o)
    {
        var errors = new List<string>();
        if (string.IsNullOrWhiteSpace(o.DatabaseUrl))
        {
            errors.Add("DATABASE_URL is required");
        }
        if (o.EmbedFailurePolicy is not ("fail" or "degrade"))
        {
            errors.Add("EMBED_FAILURE_POLICY must be fail or degrade");
        }
        if (o.PoolMaxSize < 1 || o.RetryMaxAttempts < 1 || o.RetryBaseDelaySeconds < 0)
        {
            errors.Add("POOL_MAX_SIZE and RETRY_MAX_ATTEMPTS must be positive, RETRY_BASE_DELAY not negative");
        }
        if (o.OidcIssuer is { Length: > 0 })
        {
            if (string.IsNullOrEmpty(o.OidcClientId) || string.IsNullOrEmpty(o.OidcRedirectUri))
            {
                errors.Add("OIDC_ISSUER is set but OIDC_CLIENT_ID or OIDC_REDIRECT_URI is not");
            }
            if (!GatewayOptions.IsSecureOrLoopback(o.OidcIssuer))
            {
                errors.Add("OIDC_ISSUER must be https (http only on a loopback host)");
            }
        }
        if (o.McpResource is { Length: > 0 })
        {
            if (o.AuthorizationServers.Count == 0)
            {
                errors.Add("MCP_RESOURCE is set but MCP_AUTHORIZATION_SERVERS is not");
            }
            foreach (var url in o.AuthorizationServers.Prepend(o.McpResource))
            {
                if (!GatewayOptions.IsSecureOrLoopback(url) || new Uri(url).Query.Length > 0 || new Uri(url).Fragment.Length > 0)
                {
                    errors.Add($"{url} must be https (http only on a loopback host), with no query and no fragment");
                }
            }
        }
        return errors.Count == 0 ? ValidateOptionsResult.Success : ValidateOptionsResult.Fail(errors);
    }
}
