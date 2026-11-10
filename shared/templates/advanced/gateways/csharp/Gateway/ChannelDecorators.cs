// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// Decorators of IDbChannel: each implements the interface and wraps another,
// so cross-cutting behaviour is composed once, in Program.cs, and no call
// site changes:
//
//     ConnectionRetry(SerializationRetry(Tracing(Postgres)))
//
// Both retries sit outside the tracing, so every attempt is a log line of
// its own. ADAPT: caching, metrics or a test double is one more decorator.

using System.Diagnostics;
using System.Net.Sockets;
using Npgsql;

namespace Pgmi.Gateway;

// Passes every call on; a decorator overrides only what it changes.
public abstract class DbChannelDecorator(IDbChannel inner) : IDbChannel
{
    protected IDbChannel Inner { get; } = inner;

    public virtual Task<Policy> RestPolicyAsync(RequestContext ctx, string? requestedIsolation, CancellationToken ct) =>
        Inner.RestPolicyAsync(ctx, requestedIsolation, ct);

    public virtual Task<RestResult> RestInvokeAsync(RequestContext ctx, Policy policy, IReadOnlyDictionary<string, string> headers,
                                                    byte[]? body, CancellationToken ct) =>
        Inner.RestInvokeAsync(ctx, policy, headers, body, ct);

    public virtual Task<Policy> McpPolicyAsync(RequestContext ctx, string request, string? requestedIsolation, McpHeaders headers,
                                               CancellationToken ct) =>
        Inner.McpPolicyAsync(ctx, request, requestedIsolation, headers, ct);

    public virtual Task<string?> McpRequestAsync(RequestContext ctx, Policy policy, string request, string? callerContext,
                                                 CancellationToken ct) =>
        Inner.McpRequestAsync(ctx, policy, request, callerContext, ct);

    // Full jitter: losers of a race that back off in lockstep collide again.
    protected static TimeSpan Backoff(int attempt, double baseDelaySeconds) =>
        TimeSpan.FromSeconds(Random.Shared.NextDouble() * Math.Min(1.0, baseDelaySeconds * (1 << attempt)));
}

// One log line per call with the request id, so a slow or failing request
// can be followed from the gateway log into the database's exchange log.
// Never bodies or headers: they carry personal data and session handles.
// ADAPT: an OpenTelemetry span instead (ActivitySource), same place.
public sealed class TracingChannel(IDbChannel inner, ILogger<TracingChannel> logger) : DbChannelDecorator(inner)
{
    public override Task<Policy> RestPolicyAsync(RequestContext ctx, string? requestedIsolation, CancellationToken ct) =>
        Traced(ctx, "rest.policy", () => Inner.RestPolicyAsync(ctx, requestedIsolation, ct), p => $"handler={p.Handler}");

    public override Task<RestResult> RestInvokeAsync(RequestContext ctx, Policy policy, IReadOnlyDictionary<string, string> headers,
                                                     byte[]? body, CancellationToken ct) =>
        Traced(ctx, "rest.invoke", () => Inner.RestInvokeAsync(ctx, policy, headers, body, ct), r => $"status={r.Status}");

    public override Task<Policy> McpPolicyAsync(RequestContext ctx, string request, string? requestedIsolation, McpHeaders headers,
                                                CancellationToken ct) =>
        Traced(ctx, "mcp.policy", () => Inner.McpPolicyAsync(ctx, request, requestedIsolation, headers, ct),
               p => p.Refusal is null ? $"handler={p.Handler}" : "refused");

    public override Task<string?> McpRequestAsync(RequestContext ctx, Policy policy, string request, string? callerContext,
                                                  CancellationToken ct) =>
        Traced(ctx, "mcp.request", () => Inner.McpRequestAsync(ctx, policy, request, callerContext, ct),
               e => e is null ? "notification" : "answered");

    private async Task<T> Traced<T>(RequestContext ctx, string operation, Func<Task<T>> call, Func<T, string> outcome)
    {
        var clock = Stopwatch.StartNew();
        var path = ctx.Url.Split('?', 2)[0]; // a query can carry a token
        try
        {
            var result = await call();
            logger.LogInformation("{RequestId} {Operation} {Method} {Url} {Outcome} {ElapsedMs:F1}ms",
                                  ctx.RequestId, operation, ctx.Method, path, outcome(result), clock.Elapsed.TotalMilliseconds);
            return result;
        }
        catch (Exception e)
        {
            logger.LogInformation("{RequestId} {Operation} {Method} {Url} error={Error} {ElapsedMs:F1}ms",
                                  ctx.RequestId, operation, ctx.Method, path,
                                  (e as PostgresException)?.SqlState ?? e.GetType().Name, clock.Elapsed.TotalMilliseconds);
            throw;
        }
    }
}

public sealed class RetriesExhaustedException(string sqlState, int attempts, Exception inner)
    : Exception($"serialization failure, {attempts} attempts", inner)
{
    public string SqlState { get; } = sqlState;
    public int Attempts { get; } = attempts;
}

// 40001 (serialization failure) and 40P01 (deadlock): the database rolled the
// whole transaction back, so running it again is safe for any call, writes
// included. Each attempt is a new transaction and so a fresh snapshot; a
// savepoint or a loop in PL/pgSQL keeps the old snapshot and never converges.
//
// A handler on a route that can hit 40001 must be safe to run twice: a side
// effect outside the transaction (an HTTP call, a queue publish) happens again.
public sealed class SerializationRetry(IDbChannel inner, int attempts, double baseDelaySeconds) : DbChannelDecorator(inner)
{
    public override Task<RestResult> RestInvokeAsync(RequestContext ctx, Policy policy, IReadOnlyDictionary<string, string> headers,
                                                     byte[]? body, CancellationToken ct) =>
        Retrying(() => Inner.RestInvokeAsync(ctx, policy, headers, body, ct), ct);

    public override Task<string?> McpRequestAsync(RequestContext ctx, Policy policy, string request, string? callerContext,
                                                  CancellationToken ct) =>
        Retrying(() => Inner.McpRequestAsync(ctx, policy, request, callerContext, ct), ct);

    private async Task<T> Retrying<T>(Func<Task<T>> call, CancellationToken ct)
    {
        for (var attempt = 0; ; attempt++)
        {
            try
            {
                return await call();
            }
            catch (PostgresException e) when (e.SqlState is PostgresErrorCodes.SerializationFailure or PostgresErrorCodes.DeadlockDetected)
            {
                if (attempt == attempts - 1)
                {
                    throw new RetriesExhaustedException(e.SqlState, attempts, e);
                }
                await Task.Delay(Backoff(attempt, baseDelaySeconds), ct);
            }
        }
    }
}

// Sends a call again after the connection dropped, ONLY when sending it twice
// cannot apply anything twice: the policy lookups (STABLE functions) and a
// dispatch the database runs READ ONLY (the route declared readOnly, so
// PostgreSQL refuses any write in it). A write is never re-sent: the
// connection can drop after the server committed and before the answer
// arrived, and a second send would apply it twice. MCP is never read-only
// (every exchange is logged), so its dispatch is never retried here.
//
// The HTTP method says nothing: a GET handler can write. The database's
// readOnly declaration does.
public sealed class ConnectionRetry(IDbChannel inner, ILogger<ConnectionRetry> logger, int attempts = 2, double baseDelaySeconds = 0.05)
    : DbChannelDecorator(inner)
{
    public override Task<Policy> RestPolicyAsync(RequestContext ctx, string? requestedIsolation, CancellationToken ct) =>
        Retrying(ctx, "rest.policy", () => Inner.RestPolicyAsync(ctx, requestedIsolation, ct), ct);

    public override Task<RestResult> RestInvokeAsync(RequestContext ctx, Policy policy, IReadOnlyDictionary<string, string> headers,
                                                     byte[]? body, CancellationToken ct) =>
        policy.ReadOnly
            ? Retrying(ctx, "rest.invoke", () => Inner.RestInvokeAsync(ctx, policy, headers, body, ct), ct)
            : Inner.RestInvokeAsync(ctx, policy, headers, body, ct);

    public override Task<Policy> McpPolicyAsync(RequestContext ctx, string request, string? requestedIsolation, McpHeaders headers,
                                                CancellationToken ct) =>
        Retrying(ctx, "mcp.policy", () => Inner.McpPolicyAsync(ctx, request, requestedIsolation, headers, ct), ct);

    private async Task<T> Retrying<T>(RequestContext ctx, string operation, Func<Task<T>> call, CancellationToken ct)
    {
        for (var attempt = 0; ; attempt++)
        {
            try
            {
                return await call();
            }
            catch (Exception e) when (attempt < attempts - 1 && ConnectionLost(e) && !ct.IsCancellationRequested)
            {
                logger.LogWarning("{RequestId} {Operation}: connection lost ({Error}), attempt {Attempt}",
                                  ctx.RequestId, operation, e.Message, attempt + 2);
                await Task.Delay(Backoff(attempt, baseDelaySeconds), ct);
            }
        }
    }

    // A failure of the connection, not an answer from the server: the socket
    // broke, class 08 (connection exception) or 57P01..57P03 (the server shut
    // down). Not 40001 and not DatabaseUnavailableException: an exhausted pool
    // is answered 503 at once rather than waited out twice.
    public static bool ConnectionLost(Exception e) => e switch
    {
        PostgresException p => p.SqlState.StartsWith("08") || p.SqlState is "57P01" or "57P02" or "57P03",
        NpgsqlException n => n.InnerException is IOException or SocketException or EndOfStreamException,
        _ => false,
    };
}
