// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// One OIDC issuer's discovery document and signing keys, cached. Sign-in
// (SignIn.cs) and the OAuth resource server (ResourceServer.cs) both verify
// tokens against them.
//
// Why not ConfigurationManager<OpenIdConnectConfiguration>: on an unknown key
// id its RequestRefresh() refetches on a background thread, so the request
// that saw the rotated key fails anyway; the blocking alternative is a
// process-wide AppContext switch. A key rotation must not fail a sign-in
// (pgmi's harness checks it), so this class refetches in line, at most once
// per cooldown, with the same retriever ConfigurationManager uses.

using Microsoft.IdentityModel.Protocols;
using Microsoft.IdentityModel.Protocols.OpenIdConnect;
using Microsoft.IdentityModel.Tokens;

namespace Pgmi.Gateway;

// The provider's discovery or keys could not be read, or discovery names
// another issuer. Never trusted, never cached.
public sealed class IdentityProviderUnavailableException(string reason, Exception? inner = null) : Exception(reason, inner);

public sealed class IssuerKeys(string issuer, IHttpClientFactory httpClients, TimeSpan unknownKeyCooldown)
{
    // The HttpClient name, configured in Program.cs (timeout, handlers).
    public const string HttpClientName = "identity-provider";
    private static readonly TimeSpan Lifespan = TimeSpan.FromMinutes(5);

    private sealed record Snapshot(OpenIdConnectConfiguration Configuration, DateTimeOffset FetchedAt);

    private readonly SemaphoreSlim _gate = new(1, 1);
    private volatile Snapshot? _current;
    private DateTimeOffset _lastUnknownKeyFetch = DateTimeOffset.MinValue;

    public string Issuer => issuer;

    public async Task<OpenIdConnectConfiguration> ConfigurationAsync(CancellationToken ct)
    {
        if (_current is { } fresh && DateTimeOffset.UtcNow - fresh.FetchedAt < Lifespan)
        {
            return fresh.Configuration;
        }
        await _gate.WaitAsync(ct);
        try
        {
            if (_current is { } other && DateTimeOffset.UtcNow - other.FetchedAt < Lifespan)
            {
                return other.Configuration;
            }
            return (await FetchAsync(ct)).Configuration;
        }
        finally
        {
            _gate.Release();
        }
    }

    // The key a token's kid names. An unknown kid refetches the set, unless
    // that happened less than the cooldown ago: tokens on /mcp come from any
    // client, and a flood of made-up key ids must not flood the provider.
    public async Task<SecurityKey?> SigningKeyAsync(string kid, CancellationToken ct)
    {
        var seen = await ConfigurationAsync(ct);
        if (Find(seen, kid) is { } key)
        {
            return key;
        }
        await _gate.WaitAsync(ct);
        try
        {
            if (_current is { } current && !ReferenceEquals(current.Configuration, seen))
            {
                seen = current.Configuration;
            }
            else if (DateTimeOffset.UtcNow - _lastUnknownKeyFetch >= unknownKeyCooldown)
            {
                _lastUnknownKeyFetch = DateTimeOffset.UtcNow;
                seen = (await FetchAsync(ct)).Configuration;
            }
        }
        finally
        {
            _gate.Release();
        }
        return Find(seen, kid);
    }

    private static SecurityKey? Find(OpenIdConnectConfiguration configuration, string kid) =>
        configuration.SigningKeys.FirstOrDefault(k => k.KeyId == kid);

    private async Task<Snapshot> FetchAsync(CancellationToken ct)
    {
        // http only for a loopback issuer, which GatewayOptions allows for tests.
        var retriever = new HttpDocumentRetriever(httpClients.CreateClient(HttpClientName))
        {
            RequireHttps = new Uri(issuer).Scheme == Uri.UriSchemeHttps,
        };
        OpenIdConnectConfiguration configuration;
        try
        {
            configuration = await OpenIdConnectConfigurationRetriever.GetAsync(
                issuer.TrimEnd('/') + "/.well-known/openid-configuration", retriever, ct);
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            throw new IdentityProviderUnavailableException("discovery or the key set could not be read", e);
        }
        if (configuration.Issuer != issuer || string.IsNullOrEmpty(configuration.JwksUri))
        {
            throw new IdentityProviderUnavailableException("discovery names another issuer or no jwks_uri");
        }
        var snapshot = new Snapshot(configuration, DateTimeOffset.UtcNow);
        _current = snapshot;
        return snapshot;
    }
}
