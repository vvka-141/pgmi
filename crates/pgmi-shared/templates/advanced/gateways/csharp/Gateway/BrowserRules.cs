// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// What a browser caller is held to (../../PROTOCOL.md, "Identity: handles
// and verified tokens"): the session cookies, the CSRF token, the Origin
// allowlist, and the headers the database is allowed to see. The checks are
// endpoint filters, attached to the endpoints they guard in Program.cs.

using System.Security.Cryptography;
using System.Text;
using Microsoft.AspNetCore.Http.Extensions;
using Microsoft.AspNetCore.Http.Features;
using Microsoft.AspNetCore.WebUtilities;
using Microsoft.Extensions.Options;

namespace Pgmi.Gateway;

// Standard web plumbing, not pgmi-specific: replace it with your framework's own.
// The session and CSRF cookies. The session handle reaches the database only
// from the HttpOnly cookie this gateway set; the CSRF token is an HMAC of
// that handle, readable by the page, so a cross-site page cannot forge it.
// ADAPT: cookie names and lifetime.
public sealed class SessionCookies(IOptions<GatewayOptions> options)
{
    public const string Session = "__Host-session";
    public const string Csrf = "__Host-csrf";
    public const string Login = "__Host-login";
    public const string CsrfHeader = "X-CSRF-Token";
    private static readonly TimeSpan SessionMaxAge = TimeSpan.FromDays(7);

    // LOGIN_CSRF_SECRET must be the same on every process that serves the
    // same users; unset, each process makes its own and tokens die with it.
    private readonly byte[] _secret = options.Value.CsrfSecret is { Length: > 0 } configured
        ? Encoding.UTF8.GetBytes(configured)
        : RandomNumberGenerator.GetBytes(32);

    public static string? SessionOf(HttpRequest request) => request.Cookies[Session] is { Length: > 0 } s ? s : null;

    public string CsrfToken(string session) =>
        WebEncoders.Base64UrlEncode(HMACSHA256.HashData(_secret, Encoding.UTF8.GetBytes("csrf\0" + session)));

    // A state-changing request riding on the session cookie must carry the
    // session's CSRF token.
    public bool CsrfOk(string method, string? session, string? presented)
    {
        if (HttpMethods.IsGet(method) || HttpMethods.IsHead(method) || HttpMethods.IsOptions(method) || session is null)
        {
            return true;
        }
        return !string.IsNullOrEmpty(presented)
               && CryptographicOperations.FixedTimeEquals(Encoding.UTF8.GetBytes(CsrfToken(session)), Encoding.UTF8.GetBytes(presented));
    }

    public void Issue(HttpResponse response, string session)
    {
        response.Cookies.Append(Session, session, Options(Session, SessionMaxAge, httpOnly: true));
        response.Cookies.Append(Csrf, CsrfToken(session), Options(Csrf, SessionMaxAge, httpOnly: false));
        response.Headers.CacheControl = "no-store";
    }

    public static void Clear(HttpResponse response, string name) =>
        response.Cookies.Append(name, "", Options(name, TimeSpan.Zero, httpOnly: name != Csrf));

    // SameSite=Strict keeps the session off every cross-site request, a link
    // included. The sign-in cookie is Lax: the provider's redirect back to
    // /auth/callback is a cross-site navigation that must carry it.
    public static CookieOptions Options(string name, TimeSpan maxAge, bool httpOnly) =>
        new() { Path = "/", MaxAge = maxAge, Secure = true, HttpOnly = httpOnly,
                SameSite = name == Login ? SameSiteMode.Lax : SameSiteMode.Strict };
}

public sealed class HeaderCollisionException(string name) : Exception($"header {name} is sent more than once")
{
    public string Name { get; } = name;
}

// pgmi-specific: what may reach the database; see ../../PROTOCOL.md
// "Identity: handles and verified tokens".
public static class ForwardedHeaders
{
    // Session and connection handles the gateway did not issue, the cookie
    // that carries the one it did, and hop-by-hop headers.
    private static readonly HashSet<string> NotForwarded = new(StringComparer.OrdinalIgnoreCase)
    {
        "x-login-session", "x-connection", "cookie", "x-csrf-token",
        "connection", "keep-alive", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade",
    };

    private static readonly HashSet<string> SingleValued = new(StringComparer.OrdinalIgnoreCase)
    {
        "authorization", "content-length", "content-type", "host", "origin",
        "x-csrf-token", "x-organization-session", "x-pgmi-transaction-isolation",
    };

    // Every header the client sent: lower-cased, each name once. Kestrel
    // folds X-A and x-a into one entry, so a repeat shows as two values,
    // joined (RFC 9110 §5.3). Interceptors see this, credentials included.
    public static Dictionary<string, string> Incoming(IHeaderDictionary headers) =>
        headers.ToDictionary(h => h.Key.ToLowerInvariant(),
                             h => string.Join(h.Key.Equals("cookie", StringComparison.OrdinalIgnoreCase) ? "; " : ", ", h.Value.ToArray()));

    // The headers the database sees: the incoming ones without the handles
    // and cookie the client sent, the login session from the gateway's cookie
    // only. A repeated single-valued name is refused rather than guessed at.
    public static Dictionary<string, string> From(IHeaderDictionary headers, string? loginSession)
    {
        foreach (var (name, values) in headers)
        {
            if (values.Count > 1 && SingleValued.Contains(name))
            {
                throw new HeaderCollisionException(name.ToLowerInvariant());
            }
        }
        return Reset(Incoming(headers), loginSession);
    }

    // Again after the interceptors: whatever they did, the session handle is
    // the cookie's.
    public static Dictionary<string, string> Reset(IReadOnlyDictionary<string, string> headers, string? loginSession)
    {
        var reset = headers.Where(h => !NotForwarded.Contains(h.Key))
                           .ToDictionary(h => h.Key.ToLowerInvariant(), h => h.Value);
        if (loginSession is not null)
        {
            reset["x-login-session"] = loginSession;
        }
        return reset;
    }
}

// Standard web plumbing, not pgmi-specific: replace it with your framework's own.
// DNS-rebinding protection: a browser sends Origin; any value not on the
// allowlist is refused. Clients that send no Origin are not browsers.
public sealed class OriginFilter(IOptions<GatewayOptions> options) : IEndpointFilter
{
    private readonly IReadOnlySet<string> _allowed = options.Value.Origins;

    public ValueTask<object?> InvokeAsync(EndpointFilterInvocationContext context, EndpointFilterDelegate next)
    {
        var origin = context.HttpContext.Request.Headers.Origin;
        if (origin.Count > 0 && !_allowed.Contains(origin.ToString()))
        {
            return ValueTask.FromResult<object?>(Problems.Problem(403, "Forbidden", "pgmi.origin_refused"));
        }
        return next(context);
    }
}

public sealed class CsrfFilter(SessionCookies cookies) : IEndpointFilter
{
    public ValueTask<object?> InvokeAsync(EndpointFilterInvocationContext context, EndpointFilterDelegate next)
    {
        var request = context.HttpContext.Request;
        if (!cookies.CsrfOk(request.Method, SessionCookies.SessionOf(request), request.Headers[SessionCookies.CsrfHeader].ToString()))
        {
            return ValueTask.FromResult<object?>(Problems.Problem(403, "Forbidden", "pgmi.csrf_refused",
                                                                  $"send the {SessionCookies.Csrf} value in {SessionCookies.CsrfHeader}"));
        }
        return next(context);
    }
}

// Routing matches /api case-insensitively and after percent-decoding; the
// database must see exactly what the client sent under the exact prefix, so
// /API/hello and /%61pi/hello are 404 here, not REST calls.
public sealed class RawRestPrefixFilter : IEndpointFilter
{
    public const string Prefix = "/api";

    public ValueTask<object?> InvokeAsync(EndpointFilterInvocationContext context, EndpointFilterDelegate next)
    {
        // TestServer leaves RawTarget empty; Kestrel always sets it.
        var raw = context.HttpContext.Features.Get<IHttpRequestFeature>()?.RawTarget is { Length: > 0 } target
            ? target
            : context.HttpContext.Request.GetEncodedPathAndQuery();
        if (RestUrl(raw) is not { } url)
        {
            return ValueTask.FromResult<object?>(Problems.NotFound());
        }
        context.HttpContext.Items[Prefix] = url;
        return next(context);
    }

    // The URL the database sees: the raw target after the prefix, query included.
    public static string? RestUrl(string rawTarget)
    {
        var query = rawTarget.IndexOf('?');
        var path = query < 0 ? rawTarget : rawTarget[..query];
        if (path != Prefix && !path.StartsWith(Prefix + "/", StringComparison.Ordinal))
        {
            return null;
        }
        var url = path.Length == Prefix.Length ? "/" : path[Prefix.Length..];
        return query < 0 ? url : url + rawTarget[query..];
    }
}
