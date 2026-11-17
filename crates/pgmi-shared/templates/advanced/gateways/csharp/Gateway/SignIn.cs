// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// Sign-in in front of the database (docs/advanced/LOGIN-TRANSPORT.md in the
// pgmi repository; ../../PROTOCOL.md, "Sign-in"):
//
//     GET  /auth/login?returnTo=/path   start the authorization code flow (PKCE)
//     GET  /auth/callback               check state, exchange the code, verify the
//                                       ID token, call api.open_login_session
//     POST /auth/logout                 api.close_login_session, clear the cookies
//     POST /auth/dev-login              DEV_LOGIN=1 only: api.open_dev_login_session
//
// Only the opaque login-session handle reaches the browser, in an HttpOnly
// cookie. The database decides who the caller is; nothing here names a person.
//
// Why not AddAuthentication().AddOpenIdConnect(): that handler signs people
// in to its own cookie scheme with its own correlation cookies and error
// pages. Here the session is the database's handle in __Host-session, the
// pending sign-in is __Host-login, and every refusal is a login.* problem
// code. The protocol parts it would do (discovery, keys, ID token checks)
// come from Microsoft.IdentityModel all the same.

using System.Net.Http.Headers;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Microsoft.AspNetCore.WebUtilities;
using Microsoft.Extensions.Options;
using Microsoft.IdentityModel.JsonWebTokens;
using Microsoft.IdentityModel.Tokens;
using Npgsql;
using NpgsqlTypes;

namespace Pgmi.Gateway;

public sealed class LoginFailedException(int status, string code, string detail) : Exception(detail)
{
    public int Status { get; } = status;
    public string Code { get; } = code;
}

// Previous is the handle the browser sent to /auth/login. The callback cannot
// read it: it is the provider's cross-site redirect, and the SameSite=Strict
// session cookie does not travel on it.
public sealed record PendingLogin(string State, string Nonce, string Verifier, string ReturnTo, string? Previous = null);

// Sign-ins between /auth/login and /auth/callback, each usable once, for
// MaxAge, at most limit of them (the oldest goes first). Kept in insertion
// order, which is time order, so expiry and eviction take from the front
// without scanning, all under one lock. The per-IP limit on /auth keeps one
// address from filling it.
//
// ADAPT: a single-process demo. Behind a load balancer, pin /auth/* to one
// process, keep this in a shared store, or drop the store: carry the pending
// sign-in in a signed, short-lived __Host-login cookie instead.
public sealed class PendingLogins(TimeProvider time, int limit = 10_000)
{
    public static readonly TimeSpan MaxAge = TimeSpan.FromMinutes(10);

    private sealed record Entry(string Key, long At, PendingLogin Login);

    private readonly Lock _lock = new();
    private readonly Dictionary<string, LinkedListNode<Entry>> _items = [];
    private readonly LinkedList<Entry> _byAge = new();

    public int Count
    {
        get
        {
            lock (_lock)
            {
                return _items.Count;
            }
        }
    }

    public string Put(PendingLogin login)
    {
        var key = WebEncoders.Base64UrlEncode(RandomNumberGenerator.GetBytes(32));
        lock (_lock)
        {
            var now = time.GetTimestamp();
            while (_byAge.First is { } oldest && (_byAge.Count >= limit || time.GetElapsedTime(oldest.Value.At, now) > MaxAge))
            {
                _items.Remove(oldest.Value.Key);
                _byAge.RemoveFirst();
            }
            _items[key] = _byAge.AddLast(new Entry(key, now, login));
        }
        return key;
    }

    public PendingLogin? Take(string? key)
    {
        if (key is null)
        {
            return null;
        }
        LinkedListNode<Entry>? node;
        lock (_lock)
        {
            if (_items.Remove(key, out node))
            {
                _byAge.Remove(node);
            }
        }
        return node is not null && time.GetElapsedTime(node.Value.At) <= MaxAge ? node.Value.Login : null;
    }
}

public sealed class OidcSignIn
{
    // The development issuer seeded by membership/dev/01-issuers.sql.
    // ADAPT: yours, if you rename it.
    public const string DevIssuer = "dev";

    // Standard OAuth/OIDC token verification, not pgmi-specific: replace it with
    // your own stack's verifier. The same three algorithms in every gateway.
    private static readonly string[] Algorithms = ResourceServer.Algorithms;

    private readonly GatewayOptions _options;
    private readonly IHttpClientFactory _httpClients;
    private readonly IssuerKeys? _issuer;
    private readonly PendingLogins _pending;
    private readonly JsonWebTokenHandler _handler = new();

    public OidcSignIn(IOptions<GatewayOptions> options, IHttpClientFactory httpClients, TimeProvider time)
    {
        _options = options.Value;
        _httpClients = httpClients;
        _pending = new PendingLogins(time);
        // No cooldown on an unknown key: ID tokens reach this gateway only
        // from the token endpoint, never from a client, so a rotated key is
        // fetched by the first sign-in that needs it.
        _issuer = _options.OidcIssuer is { Length: > 0 } issuer ? new IssuerKeys(issuer, httpClients, TimeSpan.Zero) : null;
    }

    public bool Configured => _issuer is not null;

    // The provider's authorization URL, and the key of the pending sign-in
    // for the __Host-login cookie.
    public async Task<(string Location, string LoginKey)> BeginAsync(string? returnTo, string? previous, CancellationToken ct)
    {
        var configuration = await ConfigurationAsync(ct);
        var verifier = WebEncoders.Base64UrlEncode(RandomNumberGenerator.GetBytes(48));
        var login = new PendingLogin(WebEncoders.Base64UrlEncode(RandomNumberGenerator.GetBytes(32)),
                                     WebEncoders.Base64UrlEncode(RandomNumberGenerator.GetBytes(32)), verifier, SafeReturnTo(returnTo), previous);
        var location = QueryHelpers.AddQueryString(configuration.AuthorizationEndpoint, new Dictionary<string, string?>
        {
            ["response_type"] = "code",
            ["client_id"] = _options.OidcClientId,
            ["redirect_uri"] = _options.OidcRedirectUri,
            ["scope"] = _options.OidcScopes,
            ["state"] = login.State,
            ["nonce"] = login.Nonce,
            ["code_challenge"] = WebEncoders.Base64UrlEncode(SHA256.HashData(Encoding.ASCII.GetBytes(verifier))),
            ["code_challenge_method"] = "S256",
        });
        return (location, _pending.Put(login));
    }

    // The callback: the pending sign-in is consumed first, so a callback
    // works once. Returns the place to go back to, the verified claims and the
    // handle the browser held when the sign-in began.
    public async Task<(string ReturnTo, JsonElement Claims, string? Previous)> CompleteAsync(string? loginKey, IQueryCollection query, CancellationToken ct)
    {
        var login = _pending.Take(loginKey);
        var state = query["state"].ToString();
        if (login is null || state.Length == 0
            || !CryptographicOperations.FixedTimeEquals(Encoding.UTF8.GetBytes(state), Encoding.UTF8.GetBytes(login.State)))
        {
            throw new LoginFailedException(400, "pgmi.login.state_invalid", "unknown, expired or replayed sign-in");
        }
        if (query.ContainsKey("error"))
        {
            var error = query["error"].ToString();
            throw new LoginFailedException(400, "pgmi.login.provider_error", error[..Math.Min(100, error.Length)]);
        }
        if (query["code"].ToString() is not { Length: > 0 } code)
        {
            throw new LoginFailedException(400, "pgmi.login.state_invalid", "the callback carries no code");
        }
        var idToken = await ExchangeAsync(code, login.Verifier, ct);
        return (login.ReturnTo, await VerifyAsync(idToken, login.Nonce, ct), login.Previous);
    }

    private async Task<Microsoft.IdentityModel.Protocols.OpenIdConnect.OpenIdConnectConfiguration> ConfigurationAsync(CancellationToken ct)
    {
        try
        {
            return await _issuer!.ConfigurationAsync(ct);
        }
        catch (IdentityProviderUnavailableException e)
        {
            throw new LoginFailedException(502, "pgmi.login.provider_unavailable", e.Message);
        }
    }

    private async Task<string> ExchangeAsync(string code, string verifier, CancellationToken ct)
    {
        var configuration = await ConfigurationAsync(ct);
        var form = new Dictionary<string, string>
        {
            ["grant_type"] = "authorization_code",
            ["code"] = code,
            ["redirect_uri"] = _options.OidcRedirectUri!,
            ["code_verifier"] = verifier,
        };
        using var request = new HttpRequestMessage(HttpMethod.Post, configuration.TokenEndpoint);
        request.Headers.Accept.ParseAdd("application/json");
        if (_options.OidcClientSecret is { Length: > 0 } secret)
        {
            // client_secret_basic: both parts form-encoded first (RFC 6749 §2.3.1).
            var pair = Uri.EscapeDataString(_options.OidcClientId!) + ":" + Uri.EscapeDataString(secret);
            request.Headers.Authorization = new AuthenticationHeaderValue("Basic", Convert.ToBase64String(Encoding.UTF8.GetBytes(pair)));
        }
        else
        {
            form["client_id"] = _options.OidcClientId!;
        }
        request.Content = new FormUrlEncodedContent(form);

        JsonElement tokens;
        try
        {
            using var response = await _httpClients.CreateClient(IssuerKeys.HttpClientName).SendAsync(request, ct);
            if (!response.IsSuccessStatusCode)
            {
                throw new LoginFailedException(401, "pgmi.login.provider_refused", $"the provider answered {(int)response.StatusCode}");
            }
            tokens = JsonDocument.Parse(await response.Content.ReadAsStringAsync(ct)).RootElement.Clone();
        }
        catch (Exception e) when (e is HttpRequestException or JsonException or TaskCanceledException && !ct.IsCancellationRequested)
        {
            throw new LoginFailedException(502, "pgmi.login.provider_unavailable", "the provider could not be reached");
        }
        return tokens.ValueKind == JsonValueKind.Object && ResourceServer.Text(tokens, "id_token") is { } idToken
            ? idToken
            : throw new LoginFailedException(401, "pgmi.login.token_invalid", "the provider returned no ID token");
    }

    private async Task<JsonElement> VerifyAsync(string idToken, string nonce, CancellationToken ct)
    {
        JsonWebToken token;
        JsonElement claims;
        try
        {
            token = new JsonWebToken(idToken);
            claims = ResourceServer.Payload(token);
        }
        catch (Exception e) when (e is ArgumentException or SecurityTokenException or JsonException or FormatException)
        {
            throw new LoginFailedException(401, "pgmi.login.token_invalid", "not a JWT");
        }
        SecurityKey key;
        try
        {
            key = (string.IsNullOrEmpty(token.Kid) ? null : await _issuer!.SigningKeyAsync(token.Kid, ct))
                  ?? throw new LoginFailedException(401, "pgmi.login.token_invalid", "unknown key");
        }
        catch (IdentityProviderUnavailableException e)
        {
            throw new LoginFailedException(502, "pgmi.login.provider_unavailable", e.Message);
        }
        var result = await _handler.ValidateTokenAsync(idToken, new TokenValidationParameters
        {
            ValidIssuer = _issuer!.Issuer,
            ValidAudience = _options.OidcClientId,
            IssuerSigningKey = key,
            TryAllIssuerSigningKeys = false,
            ValidAlgorithms = Algorithms,
            ClockSkew = ResourceServer.ClockSkew,
            RequireExpirationTime = true,
            RequireSignedTokens = true,
        });
        if (!result.IsValid)
        {
            throw new LoginFailedException(401, "pgmi.login.token_invalid", result.Exception?.GetType().Name ?? "invalid");
        }
        if (!ResourceServer.HasNumericTimesIssuedInThePast(claims))
        {
            throw new LoginFailedException(401, "pgmi.login.token_invalid", "a time claim is not a number, or iat is missing or in the future");
        }
        if (claims.TryGetProperty("aud", out var audience) && audience.ValueKind == JsonValueKind.Array && audience.GetArrayLength() > 1
            && ResourceServer.Text(claims, "azp") != _options.OidcClientId)
        {
            throw new LoginFailedException(401, "pgmi.login.token_invalid", "azp does not name this client");
        }
        if (ResourceServer.Text(claims, "nonce") is not { } sent
            || !CryptographicOperations.FixedTimeEquals(Encoding.UTF8.GetBytes(sent), Encoding.UTF8.GetBytes(nonce)))
        {
            throw new LoginFailedException(401, "pgmi.login.token_invalid", "nonce mismatch");
        }
        if (ResourceServer.Text(claims, "sub") is null)
        {
            throw new LoginFailedException(401, "pgmi.login.token_invalid", "sub is not a string");
        }
        return claims;
    }

    // Only a path on this site: no scheme, no //host, no backslash, nothing
    // outside printable ASCII (a CR or LF would split the Location header).
    // ADAPT: an application points this at its own start page. The API
    // explorer shows the signed-in routes, so it confirms the sign-in.
    const string DefaultReturnTo = "/api/docs";

    public static string SafeReturnTo(string? value) =>
        value is null || !value.StartsWith('/') || value.StartsWith("//") || value.Contains('\\') || value.Any(c => c is < '!' or > '~')
            ? DefaultReturnTo
            : value;
}

// The database side of sign-in, on pooled connections like every call.
public sealed class LoginSessions(NpgsqlDataSource dataSource)
{
    // api.open_login_session from verified ID token claims. email_verified
    // and amr go through as the provider sent them; a value of the wrong
    // JSON type becomes NULL, which the database reads as unverified and as
    // no second factor. Returns the handle, or the database's refusal code.
    public async Task<(string? Token, string? Problem)> OpenAsync(JsonElement claims, CancellationToken ct)
    {
        bool? verified = claims.TryGetProperty("email_verified", out var v) && v.ValueKind is JsonValueKind.True or JsonValueKind.False
            ? v.GetBoolean() : null;
        string[]? amr = claims.TryGetProperty("amr", out var a) && a.ValueKind == JsonValueKind.Array
                        && a.EnumerateArray().All(e => e.ValueKind == JsonValueKind.String)
            ? a.EnumerateArray().Select(e => e.GetString()!).ToArray() : null;
        await using var command = dataSource.CreateCommand(
            "SELECT token, problem FROM api.open_login_session($1, $2, $3, $4, $5, $6)");
        command.Parameters.Add(Text(ResourceServer.Text(claims, "iss")));
        command.Parameters.Add(Text(ResourceServer.Text(claims, "sub")));
        command.Parameters.Add(Text(ResourceServer.Text(claims, "email")));
        command.Parameters.Add(Text(ResourceServer.Text(claims, "name")));
        command.Parameters.Add(new NpgsqlParameter { Value = (object?)verified ?? DBNull.Value, NpgsqlDbType = NpgsqlDbType.Boolean });
        command.Parameters.Add(new NpgsqlParameter { Value = (object?)amr ?? DBNull.Value, NpgsqlDbType = NpgsqlDbType.Array | NpgsqlDbType.Text });
        await using var reader = await command.ExecuteReaderAsync(ct);
        await reader.ReadAsync(ct);
        return (reader.IsDBNull(0) ? null : reader.GetString(0), reader.IsDBNull(1) ? null : reader.GetString(1));
    }

    // Null: the database refused (the issuer is not a development one).
    public async Task<string?> OpenDevAsync(string subject, string email, string? displayName, CancellationToken ct)
    {
        await using var command = dataSource.CreateCommand("SELECT api.open_dev_login_session($1, $2, $3, $4)");
        command.Parameters.Add(Text(OidcSignIn.DevIssuer));
        command.Parameters.Add(Text(subject));
        command.Parameters.Add(Text(email));
        command.Parameters.Add(Text(displayName));
        return await command.ExecuteScalarAsync(ct) as string;
    }

    // Each sign-in gets a new handle; the one the browser held before is
    // closed, so a planted handle never becomes a signed-in session.
    public async Task CloseAsync(string? session, CancellationToken ct)
    {
        if (session is null)
        {
            return;
        }
        await using var command = dataSource.CreateCommand("SELECT api.close_login_session($1)");
        command.Parameters.Add(Text(session));
        await command.ExecuteNonQueryAsync(ct);
    }

    private static NpgsqlParameter Text(string? value) =>
        new() { Value = (object?)value ?? DBNull.Value, NpgsqlDbType = NpgsqlDbType.Text };
}

public sealed class SignInEndpoints
{
    private sealed record DevLoginRequest(string Subject, string Email, string? DisplayName);

    public static async Task<IResult> LoginAsync(HttpContext http, OidcSignIn oidc, string? returnTo)
    {
        if (!oidc.Configured)
        {
            return Problems.NotFound();
        }
        try
        {
            var (location, key) = await oidc.BeginAsync(returnTo, SessionCookies.SessionOf(http.Request), http.RequestAborted);
            http.Response.Cookies.Append(SessionCookies.Login, key, SessionCookies.Options(SessionCookies.Login, PendingLogins.MaxAge, httpOnly: true));
            http.Response.Headers.CacheControl = "no-store";
            return Results.Redirect(location);
        }
        catch (LoginFailedException e)
        {
            return Problems.Problem(e.Status, "Sign-in failed", e.Code);
        }
    }

    public static async Task<IResult> CallbackAsync(HttpContext http, OidcSignIn oidc, LoginSessions sessions, SessionCookies cookies,
                                                    ILogger<SignInEndpoints> logger)
    {
        if (!oidc.Configured)
        {
            return Problems.NotFound();
        }
        var ct = http.RequestAborted;
        SessionCookies.Clear(http.Response, SessionCookies.Login);
        try
        {
            var (returnTo, claims, previous) = await oidc.CompleteAsync(http.Request.Cookies[SessionCookies.Login], http.Request.Query, ct);
            var (token, refused) = await sessions.OpenAsync(claims, ct);
            if (token is null)
            {
                throw new LoginFailedException(403, refused ?? "membership.login.refused", "the database refused the sign-in");
            }
            await sessions.CloseAsync(previous, ct);
            cookies.Issue(http.Response, token);
            http.Response.Headers.Location = returnTo;
            return Results.StatusCode(StatusCodes.Status303SeeOther);
        }
        catch (LoginFailedException e)
        {
            logger.LogWarning("sign-in refused: {Code} ({Detail})", e.Code, e.Message);
            return Problems.Problem(e.Status, "Sign-in failed", e.Code);
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            logger.LogError(e, "sign-in error");
            return Problems.Problem(500, "Internal Server Error", "pgmi.internal_error");
        }
    }

    public static async Task<IResult> LogoutAsync(HttpContext http, LoginSessions sessions, IOptions<GatewayOptions> options)
    {
        if (!options.Value.SignInEnabled)
        {
            return Problems.NotFound();
        }
        await sessions.CloseAsync(SessionCookies.SessionOf(http.Request), http.RequestAborted);
        SessionCookies.Clear(http.Response, SessionCookies.Session);
        SessionCookies.Clear(http.Response, SessionCookies.Csrf);
        http.Response.Headers.CacheControl = "no-store";
        return Results.NoContent();
    }

    // Development sign-in in place of an identity provider: 404 unless
    // DEV_LOGIN=1, and the database has the function only in an env=dev
    // deploy, so both must agree. Production never serves it.
    public static async Task<IResult> DevLoginAsync(HttpContext http, LoginSessions sessions, SessionCookies cookies,
                                                    IOptions<GatewayOptions> options)
    {
        if (!options.Value.DevLogin)
        {
            return Problems.NotFound();
        }
        DevLoginRequest? input;
        try
        {
            input = await JsonSerializer.DeserializeAsync<DevLoginRequest>(http.Request.Body, JsonSerializerOptions.Web, http.RequestAborted);
        }
        catch (JsonException)
        {
            input = null;
        }
        if (input is not { Subject: not null, Email: not null })
        {
            return Problems.Problem(400, "Bad Request", "pgmi.login.request_invalid", "send {\"subject\", \"email\", \"displayName\"} as JSON strings");
        }
        if (await sessions.OpenDevAsync(input.Subject, input.Email, input.DisplayName, http.RequestAborted) is not { } token)
        {
            return Problems.Problem(403, "Sign-in failed", "membership.login.refused");
        }
        await sessions.CloseAsync(SessionCookies.SessionOf(http.Request), http.RequestAborted);
        cookies.Issue(http.Response, token);
        return Results.NoContent();
    }
}
