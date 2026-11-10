// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// REST under /api: one request, one transaction (../../PROTOCOL.md, "One
// request, one transaction"). Origin, CSRF and the raw prefix are endpoint
// filters (Program.cs); this is the rest:
//
//     headers -> policy -> request interceptors -> invoke -> response interceptors

using Microsoft.Extensions.Options;

namespace Pgmi.Gateway;

public sealed class RestEndpoint
{
    public static async Task<IResult> HandleAsync(HttpContext http, IDbChannel channel, InterceptorPipeline pipeline,
                                                  IOptions<GatewayOptions> options, ILogger<RestEndpoint> logger)
    {
        var request = http.Request;
        var ct = http.RequestAborted;
        var url = (string)http.Items[RawRestPrefixFilter.Prefix]!;
        var session = SessionCookies.SessionOf(request);

        Dictionary<string, string> headers;
        try
        {
            headers = ForwardedHeaders.From(request.Headers, session);
        }
        catch (HeaderCollisionException e)
        {
            return Problems.Problem(400, "Bad Request", "pgmi.header_collision", e.Message);
        }

        // Escalation, not obligation: the database resolves max(route floor,
        // this). An unknown value is refused before the database is called.
        string? requested = null;
        if (headers.TryGetValue("x-pgmi-transaction-isolation", out var isolation)
            && (requested = PostgresChannel.NormalizeIsolation(isolation)) is null)
        {
            return Problems.Problem(400, "Bad Request", "pgmi.transaction_isolation_unsupported");
        }

        // Kestrel enforces GatewayOptions.MaxBodyBytes and a minimum data
        // rate while this reads: 413 and 408 (GatewayExceptionHandler).
        byte[]? body = null;
        if (request.ContentLength is > 0 || request.Headers.TransferEncoding.Count > 0)
        {
            using var buffer = new MemoryStream();
            await request.Body.CopyToAsync(buffer, ct);
            body = buffer.Length > 0 ? buffer.ToArray() : null;
        }

        var ctx = RequestContext.New("rest", request.Method, url, options.Value.RequestDeadline);
        RestResult? result = null;
        try
        {
            // pgmi-specific: the database decides, in its own short call before the
            // transaction opens; see ../../PROTOCOL.md "One request, one transaction".
            var policy = await channel.RestPolicyAsync(ctx, requested, ct);
            ctx = ctx.WithPolicy(policy);
            if (pipeline.RequestsFor(ctx) is { Count: > 0 } before)
            {
                // The policy call gave its connection back: none is held while
                // an interceptor waits on another service.
                var intercepted = new InterceptedRequest(request.Method, url, ForwardedHeaders.Incoming(request.Headers), body);
                await pipeline.RunAsync(before, intercepted, ctx, ct);
                headers = ForwardedHeaders.Reset(intercepted.Headers, session);
                body = intercepted.Body;
                if (intercepted.Method != ctx.Method || intercepted.Url != ctx.Url)
                {
                    ctx = ctx with { Method = intercepted.Method, Url = intercepted.Url };
                    policy = await channel.RestPolicyAsync(ctx, requested, ct);
                    ctx = ctx.WithPolicy(policy);
                }
            }
            result = await channel.RestInvokeAsync(ctx, policy, headers, body, ct);
            if (pipeline.ResponsesFor(ctx) is { Count: > 0 } after)
            {
                var intercepted = new InterceptedResponse(result.Status, result.Headers, result.Content);
                await pipeline.RunAsync(after, intercepted, ctx, ct);
                result = new RestResult(intercepted.Status, intercepted.Headers.ToDictionary(h => h.Key.ToLowerInvariant(), h => h.Value),
                                        intercepted.Body ?? []);
            }
        }
        catch (RetriesExhaustedException e)
        {
            // The transaction lost a race every time: nothing was applied, and
            // the same request may succeed when repeated.
            logger.LogWarning("{RequestId} serialization failure, retries exhausted: {SqlState}", ctx.RequestId, e.SqlState);
            return Problems.Problem(409, "Conflict", "pgmi.transaction_retryable",
                                    "the request conflicted with a concurrent change; repeat it")
                .WithHeader(http, "Retry-After", "1");
        }
        catch (DeadlineExceededException)
        {
            logger.LogWarning("{RequestId} deadline exceeded after {Seconds} s", ctx.RequestId, options.Value.RequestDeadlineSeconds);
            return Problems.Problem(504, "Gateway Timeout", "pgmi.deadline_exceeded",
                                    "the request ran past its deadline and was cancelled")
                .WithHeader(http, "X-Request-Id", ctx.RequestId);
        }
        catch (InterceptorFailedException)
        {
            // After the commit the request was applied: say so, so the client
            // does not repeat a write that already happened.
            return Problems.Problem(502, "Bad Gateway", "pgmi.interceptor_failed",
                                    result is null ? "the request was not applied"
                                                   : "the request was applied; its answer could not be completed")
                .WithHeader(http, "X-Request-Id", ctx.RequestId);
        }
        return new DatabaseAnswer(result);
    }
}

// The database's answer as it is: its status, every header it set except
// hop-by-hop ones, its bytes. HEAD keeps the length GET would have sent
// (RFC 9110 §8.6) and sends no content.
public sealed class DatabaseAnswer(RestResult result) : IResult
{
    private static readonly HashSet<string> NotCopied = new(StringComparer.OrdinalIgnoreCase)
    {
        "content-length", "transfer-encoding", "connection",
    };

    public async Task ExecuteAsync(HttpContext http)
    {
        var response = http.Response;
        response.StatusCode = result.Status;
        foreach (var (name, value) in result.Headers)
        {
            if (!NotCopied.Contains(name))
            {
                response.Headers[name] = value;
            }
        }
        // 1xx, 204 and 304 carry no content and no Content-Length (RFC 9110 §8.6).
        if (result.Status is < 200 or 204 or 304)
        {
            return;
        }
        if (HttpMethods.IsHead(http.Request.Method))
        {
            if (result.Headers.TryGetValue("content-length", out var length) && long.TryParse(length, out var getLength))
            {
                response.ContentLength = getLength;
            }
            return;
        }
        response.ContentLength = result.Content.Length;
        await response.Body.WriteAsync(result.Content, http.RequestAborted);
    }
}
