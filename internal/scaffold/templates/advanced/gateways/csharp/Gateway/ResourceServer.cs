// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// /mcp as an OAuth 2.1 resource server, and the rate limits in front of it
// (../../PROTOCOL.md, "MCP clients with OAuth", "Rate limits"). An MCP client
// signs in at the identity provider and sends `Authorization: Bearer <access
// token>`; the token is checked here and the database gets only what it
// proved: issuer, subject, client id. The token is never forwarded, stored
// or logged. The database decides who that is and whether the client may act.
//
// Why not AddAuthentication().AddJwtBearer(): it answers its own 401s and
// challenges, and the contract needs a cheap refusal before any key fetch,
// a strict single audience, the web client's id refused and a JSON-RPC 503
// when the provider is down. Those are a few lines here; the cryptography is
// Microsoft.IdentityModel's JsonWebTokenHandler either way.

using System.Text;
using System.Text.Json;
using Microsoft.Extensions.Options;
using Microsoft.IdentityModel.JsonWebTokens;
using Microsoft.IdentityModel.Tokens;

namespace Pgmi.Gateway;

// 401. Error is "invalid_token", or null when no token was sent: RFC 6750
// §3.1 sends no error code to a request without credentials.
public sealed class TokenRejectedException(string? error, string reason) : Exception(reason)
{
    public string? Error { get; } = error;
}

public sealed record VerifiedClient(string Issuer, string Subject, string ClientId)
{
    // The MCP context api.mcp_handle_request resolves, and nothing else.
    public string Context => JsonSerializer.Serialize(new { client = new { issuer = Issuer, subject = Subject, clientId = ClientId } });

    public string RateLimitKey => $"{Issuer}\n{Subject}\n{ClientId}";
}

public sealed class ResourceServer
{
    // Standard OAuth/OIDC token verification, not pgmi-specific: replace it with
    // your own stack's verifier. The same three algorithms in every gateway.
    public static readonly string[] Algorithms =
        [SecurityAlgorithms.RsaSha256, SecurityAlgorithms.RsaSsaPssSha256, SecurityAlgorithms.EcdsaSha256];
    public static readonly TimeSpan ClockSkew = TimeSpan.FromSeconds(60);
    private const int MaxTokenLength = 8192;
    private const string MetadataPath = "/.well-known/oauth-protected-resource";

    private readonly string _resource = "";
    private readonly IReadOnlyDictionary<string, IssuerKeys> _issuers = new Dictionary<string, IssuerKeys>();
    private readonly IReadOnlyList<string> _scopes;
    private readonly HashSet<string> _refusedClients = [];
    private readonly JsonWebTokenHandler _handler = new();

    public ResourceServer(IOptions<GatewayOptions> options, IHttpClientFactory httpClients)
    {
        var o = options.Value;
        _scopes = GatewayOptions.Split(o.McpScopes, ' ');
        if (o.McpResource is not { Length: > 0 } resource)
        {
            return;
        }
        Enabled = true;
        _resource = resource;
        // A token naming an unknown key refetches the issuer's key set at
        // most once per 10 s, so made-up key ids cannot flood the provider.
        _issuers = o.AuthorizationServers.ToDictionary(s => s, s => new IssuerKeys(s, httpClients, TimeSpan.FromSeconds(10)));
        // ADAPT: every client id that is not an MCP client. A token issued to
        // the web sign-in is a person's browser session, never an agent.
        if (o.OidcClientId is { Length: > 0 } web)
        {
            _refusedClients.Add(web);
        }
        var uri = new Uri(resource);
        var path = uri.AbsolutePath.TrimEnd('/');
        MetadataPaths = new HashSet<string> { MetadataPath, MetadataPath + path };
        MetadataUrl = uri.GetLeftPart(UriPartial.Authority) + MetadataPath + path;
    }

    public bool Enabled { get; }
    public IReadOnlySet<string> MetadataPaths { get; } = new HashSet<string>();
    public string MetadataUrl { get; } = "";

    // RFC 9728 §2. The MCP spec requires authorization_servers.
    public object Metadata() => new Dictionary<string, object>
    {
        ["resource"] = _resource,
        ["authorization_servers"] = _issuers.Keys.ToArray(),
        ["bearer_methods_supported"] = new[] { "header" },
        ["scopes_supported"] = _scopes,
    };

    // The 401 challenge that tells an MCP client where the metadata is
    // (RFC 9728 §5.1); without MCP_RESOURCE only the error.
    public string Challenge(string? error)
    {
        if (!Enabled)
        {
            return error is null ? "Bearer" : $"Bearer error=\"{error}\"";
        }
        var value = new StringBuilder($"Bearer resource_metadata=\"{MetadataUrl}\"");
        if (_scopes.Count > 0)
        {
            value.Append($", scope=\"{string.Join(' ', _scopes)}\"");
        }
        if (error is not null)
        {
            value.Append($", error=\"{error}\"");
        }
        return value.ToString();
    }

    // The client a valid access token proves, or TokenRejectedException /
    // IdentityProviderUnavailableException. Cheap checks first: a token that
    // is not a JWT, names an issuer not configured or another audience is
    // refused before any key is fetched.
    public async Task<VerifiedClient> VerifyAsync(string? authorization, CancellationToken ct)
    {
        if (authorization is null)
        {
            throw new TokenRejectedException(null, "no token");
        }
        var parts = authorization.Split(' ', 2);
        var token = parts.Length == 2 ? parts[1].Trim() : "";
        if (!parts[0].Equals("Bearer", StringComparison.OrdinalIgnoreCase) || token.Length is 0 or > MaxTokenLength)
        {
            throw new TokenRejectedException("invalid_token", "not a bearer token");
        }
        JsonWebToken unverified;
        JsonElement claims;
        try
        {
            unverified = new JsonWebToken(token);
            claims = Payload(unverified);
        }
        catch (Exception e) when (e is ArgumentException or SecurityTokenException or JsonException or FormatException)
        {
            throw new TokenRejectedException("invalid_token", "not a JWT");
        }
        if (Text(claims, "iss") is not { } iss || !_issuers.TryGetValue(iss, out var issuer))
        {
            throw new TokenRejectedException("invalid_token", "issuer not accepted");
        }
        if (!Algorithms.Contains(unverified.Alg) || string.IsNullOrEmpty(unverified.Kid))
        {
            throw new TokenRejectedException("invalid_token", "algorithm or key id not accepted");
        }
        // RFC 8707: issued for this resource and only this one. A token that
        // also names another audience could be replayed here by that server.
        if (!IsThisResource(claims))
        {
            throw new TokenRejectedException("invalid_token", "audience is not this resource");
        }

        var key = await issuer.SigningKeyAsync(unverified.Kid, ct)
                  ?? throw new TokenRejectedException("invalid_token", "unknown key");
        var result = await _handler.ValidateTokenAsync(token, new TokenValidationParameters
        {
            ValidIssuer = issuer.Issuer,
            ValidateAudience = false,
            IssuerSigningKey = key,
            TryAllIssuerSigningKeys = false,
            ValidAlgorithms = Algorithms,
            ClockSkew = ClockSkew,
            RequireExpirationTime = true,
            RequireSignedTokens = true,
        });
        if (!result.IsValid)
        {
            throw new TokenRejectedException("invalid_token", result.Exception?.GetType().Name ?? "invalid");
        }
        if (!HasNumericTimesIssuedInThePast(claims))
        {
            throw new TokenRejectedException("invalid_token", "a time claim is not a number, or iat is missing or in the future");
        }
        var clientId = claims.TryGetProperty("client_id", out _) ? Text(claims, "client_id") : Text(claims, "azp");
        if (string.IsNullOrEmpty(clientId) || _refusedClients.Contains(clientId))
        {
            throw new TokenRejectedException("invalid_token", "no MCP client id");
        }
        if (Text(claims, "sub") is not { Length: > 0 } subject)
        {
            throw new TokenRejectedException("invalid_token", "sub is not a string");
        }
        return new VerifiedClient(issuer.Issuer, subject, clientId);
    }

    private bool IsThisResource(JsonElement claims)
    {
        if (!claims.TryGetProperty("aud", out var audience))
        {
            return false;
        }
        if (audience.ValueKind == JsonValueKind.Array && audience.GetArrayLength() == 1)
        {
            audience = audience[0];
        }
        return audience.ValueKind == JsonValueKind.String && Canonical(audience.GetString()!) == Canonical(_resource);
    }

    // Scheme and host lower-cased, the rest as written: the MCP spec asks
    // servers to accept an upper-case scheme or host in an audience.
    public static string Canonical(string uri)
    {
        var scheme = uri.IndexOf("://", StringComparison.Ordinal);
        if (scheme < 0)
        {
            return uri;
        }
        var rest = uri[(scheme + 3)..];
        var end = rest.IndexOfAny(['/', '?', '#']);
        var authority = end < 0 ? rest : rest[..end];
        return uri[..scheme].ToLowerInvariant() + "://" + authority.ToLowerInvariant() + (end < 0 ? "" : rest[end..]);
    }

    // The claims as the token carries them, JSON types intact: the checks
    // below and the database must not see a "true" string read as true.
    public static JsonElement Payload(JsonWebToken token) =>
        JsonDocument.Parse(Base64UrlEncoder.DecodeBytes(token.EncodedPayload)).RootElement.Clone();

    public static string? Text(JsonElement claims, string name) =>
        claims.TryGetProperty(name, out var value) && value.ValueKind == JsonValueKind.String ? value.GetString() : null;

    public static bool HasNumericTimesIssuedInThePast(JsonElement claims) =>
        new[] { "exp", "nbf" }.All(name => !claims.TryGetProperty(name, out var time) || time.ValueKind == JsonValueKind.Number)
        && claims.TryGetProperty("iat", out var iat) && iat.ValueKind == JsonValueKind.Number
        && iat.GetDouble() <= DateTimeOffset.UtcNow.Add(ClockSkew).ToUnixTimeSeconds();
}

// Standard web plumbing, not pgmi-specific: replace it with your framework's own.
// A token bucket per key, refilled continuously to perMinute a minute, behind
// one lock, at most maxKeys keys (least recently seen goes first; a key that
// comes back starts full, which only an attacker holding more than maxKeys
// addresses gains from). Per process: N gateway processes allow N times the
// limit. System.Threading.RateLimiting's PartitionedRateLimiter is not used:
// it refills in steps on a timer and bounds its partitions by idle time, not
// by count.
public sealed class TokenBuckets(int perMinute, TimeProvider time, int maxKeys = 10_000)
{
    private sealed class Bucket(string key)
    {
        public string Key { get; } = key;
        public double Tokens;
        public long Seen;
    }

    private readonly Lock _lock = new();
    private readonly Dictionary<string, LinkedListNode<Bucket>> _buckets = [];
    private readonly LinkedList<Bucket> _byAge = new();
    private readonly double _perSecond = perMinute / 60.0;

    // (true, 0), or (false, seconds until the next request is allowed).
    public (bool Allowed, int RetryAfterSeconds) TryTake(string key)
    {
        if (perMinute <= 0)
        {
            return (true, 0);
        }
        var now = time.GetTimestamp();
        bool allowed;
        double tokens;
        lock (_lock)
        {
            if (_buckets.TryGetValue(key, out var node))
            {
                _byAge.Remove(node);
                node.Value.Tokens = Math.Min(perMinute, node.Value.Tokens + time.GetElapsedTime(node.Value.Seen, now).TotalSeconds * _perSecond);
            }
            else
            {
                node = new LinkedListNode<Bucket>(new Bucket(key) { Tokens = perMinute });
                _buckets[key] = node;
            }
            allowed = node.Value.Tokens >= 1;
            if (allowed)
            {
                node.Value.Tokens -= 1;
            }
            tokens = node.Value.Tokens;
            node.Value.Seen = now;
            _byAge.AddLast(node);
            while (_buckets.Count > maxKeys)
            {
                _buckets.Remove(_byAge.First!.Value.Key);
                _byAge.RemoveFirst();
            }
        }
        return allowed ? (true, 0) : (false, Math.Max(1, (int)Math.Ceiling((1 - tokens) / _perSecond)));
    }
}

// The two limits: per client address on /mcp, /auth/login and /auth/callback,
// one limit across them; per verified OAuth client on /mcp. ADAPT: behind a
// reverse proxy the client address is the proxy's; key on the address it
// forwards (UseForwardedHeaders with KnownProxies), or limit at the proxy and
// set RATE_LIMIT_PER_IP=0 here.
public sealed class RateLimits(IOptions<GatewayOptions> options, TimeProvider time)
{
    public TokenBuckets PerIp { get; } = new(options.Value.RateLimitPerIp, time);
    public TokenBuckets PerClient { get; } = new(options.Value.RateLimitPerClient, time);

    public static IResult TooMany(HttpContext http, int retryAfter) =>
        Problems.Problem(429, "Too Many Requests", "pgmi.rate_limited").WithHeader(http, "Retry-After", retryAfter.ToString());
}

// First on /mcp and /auth, before anything costs: a flood of forged tokens
// stops here and never reaches the identity provider's key set, and a flood
// of /auth/login cannot push real sign-ins out of PendingLogins.
public sealed class PerIpRateLimitFilter(RateLimits limits) : IEndpointFilter
{
    public ValueTask<object?> InvokeAsync(EndpointFilterInvocationContext context, EndpointFilterDelegate next)
    {
        var address = context.HttpContext.Connection.RemoteIpAddress?.ToString() ?? "";
        var (allowed, retryAfter) = limits.PerIp.TryTake(address);
        return allowed ? next(context) : ValueTask.FromResult<object?>(RateLimits.TooMany(context.HttpContext, retryAfter));
    }
}
