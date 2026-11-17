// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// The database channel: every call the gateway makes to PostgreSQL for a
// request, behind one interface (../../PROTOCOL.md, "Extension seams").
//
//     IDbChannel              one method per call: the policy and the dispatch
//                             of each protocol the gateway serves
//     PostgresChannel         the real thing, over the NpgsqlDataSource pool
//     ChannelDecorators.cs    tracing, serialization retry, connection retry
//
// Program.cs composes the decorators once; every request shares the result.
// Per-request data travels in the arguments, never in a field.

using System.Data;
using System.Text.Json;
using Npgsql;
using NpgsqlTypes;

namespace Pgmi.Gateway;

// What every channel call and every interceptor may read about the request.
// Immutable: a change is a new record (`with`). There is no principal: the
// gateway forwards handles and the database resolves who is calling inside
// the transaction. Handler and Annotations come from the policy call.
public sealed record RequestContext(string RequestId, string Protocol, string Method, string Url, DateTimeOffset Deadline)
{
    public string? Handler { get; init; }

    // {property: {"x-keyword": value}} from the handler's inputSchema.
    public IReadOnlyDictionary<string, JsonElement> Annotations { get; init; } = Policy.NoAnnotations;

    public TimeSpan Remaining => TimeSpan.FromTicks(Math.Max(0, (Deadline - DateTimeOffset.UtcNow).Ticks));

    public RequestContext WithPolicy(Policy policy) => this with { Handler = policy.Handler, Annotations = policy.Annotations };

    public static RequestContext New(string protocol, string method, string url, TimeSpan deadline) =>
        new(Guid.NewGuid().ToString("N"), protocol, method, url, DateTimeOffset.UtcNow + deadline);
}

// One row of a policy function. Isolation null: the connection default.
// Modern and Refusal are MCP's: the 2026-07-28 era, and the JSON-RPC answer
// to send instead of dispatching.
public sealed record Policy(string? Isolation = null, bool ReadOnly = false, bool Deferrable = false)
{
    public static readonly IReadOnlyDictionary<string, JsonElement> NoAnnotations = new Dictionary<string, JsonElement>();

    public string? Handler { get; init; }
    public IReadOnlyDictionary<string, JsonElement> Annotations { get; init; } = NoAnnotations;
    public bool Modern { get; init; }
    public string? Refusal { get; init; }
}

public sealed record McpHeaders(string? ProtocolVersion, string? Method, string? Name);

// Header names lower-cased.
public sealed record RestResult(int Status, IReadOnlyDictionary<string, string> Headers, byte[] Content);

// ADAPT: a gateway that serves JSON-RPC adds RpcPolicyAsync / RpcInvokeAsync
// here (api.rpc_route_policy, api.rpc_invoke), and every decorator gets them.
public interface IDbChannel
{
    Task<Policy> RestPolicyAsync(RequestContext ctx, string? requestedIsolation, CancellationToken ct);

    Task<RestResult> RestInvokeAsync(RequestContext ctx, Policy policy, IReadOnlyDictionary<string, string> headers,
                                     byte[]? body, CancellationToken ct);

    Task<Policy> McpPolicyAsync(RequestContext ctx, string request, string? requestedIsolation, McpHeaders headers,
                                CancellationToken ct);

    // The JSON-RPC envelope, or null for a notification.
    Task<string?> McpRequestAsync(RequestContext ctx, Policy policy, string request, string? callerContext,
                                  CancellationToken ct);
}

// Each policy call runs on a pooled connection in an implicit transaction of
// its own; each dispatch in one transaction opened with the resolved
// characteristics. The characteristics and the statement_timeout are
// transaction-scoped, so a pooled connection carries nothing to the next
// request. All SQL is fixed text; request data goes only into parameters.
//
// The policy costs one round trip per request and changes only at deploy
// time: a caching decorator would key it on api.catalog_version().
public sealed class PostgresChannel(NpgsqlDataSource dataSource) : IDbChannel
{
    private static readonly Dictionary<string, IsolationLevel> Isolation = new()
    {
        ["read committed"] = IsolationLevel.ReadCommitted,
        ["repeatable read"] = IsolationLevel.RepeatableRead,
        ["serializable"] = IsolationLevel.Serializable,
    };

    public Task<Policy> RestPolicyAsync(RequestContext ctx, string? requestedIsolation, CancellationToken ct) =>
        RunAsync(ctx, null, new NpgsqlBatchCommand(
            "SELECT transaction_isolation, transaction_read_only, transaction_deferrable, handler_name, input_annotations::text" +
            " FROM api.rest_route_policy($1, $2, $3)")
        {
            Parameters = { Text(ctx.Method), Text(ctx.Url), Text(requestedIsolation) },
        }, async (reader, token) => !await reader.ReadAsync(token)
            ? new Policy()
            : new Policy(Nullable(reader, 0), reader.GetBoolean(1), reader.GetBoolean(2))
            {
                Handler = Nullable(reader, 3),
                Annotations = ParseAnnotations(Nullable(reader, 4)),
            }, ct);

    public Task<RestResult> RestInvokeAsync(RequestContext ctx, Policy policy, IReadOnlyDictionary<string, string> headers,
                                            byte[]? body, CancellationToken ct) =>
        RunAsync(ctx, policy, new NpgsqlBatchCommand(
            "SELECT r.status_code, extensions.hstore_to_json(r.headers)::text, r.content" +
            " FROM api.rest_invoke($1, $2, extensions.hstore($3::text[], $4::text[]), $5::bytea) r")
        {
            Parameters =
            {
                Text(ctx.Method), Text(ctx.Url),
                new NpgsqlParameter { Value = headers.Keys.ToArray() },
                new NpgsqlParameter { Value = headers.Values.ToArray() },
                new NpgsqlParameter { Value = (object?)body ?? DBNull.Value, NpgsqlDbType = NpgsqlDbType.Bytea },
            },
        }, async (reader, token) =>
        {
            await reader.ReadAsync(token);
            var responseHeaders = reader.IsDBNull(1)
                ? new Dictionary<string, string>()
                : JsonSerializer.Deserialize<Dictionary<string, string>>(reader.GetString(1))!
                    .ToDictionary(h => h.Key.ToLowerInvariant(), h => h.Value);
            return new RestResult(Convert.ToInt32(reader.GetValue(0)), responseHeaders,
                                  reader.IsDBNull(2) ? [] : (byte[])reader.GetValue(2));
        }, ct);

    public Task<Policy> McpPolicyAsync(RequestContext ctx, string request, string? requestedIsolation, McpHeaders headers,
                                       CancellationToken ct) =>
        RunAsync(ctx, null, new NpgsqlBatchCommand(
            "SELECT transaction_isolation, transaction_read_only, transaction_deferrable, handler_name, input_annotations::text," +
            " modern, refusal::text FROM api.mcp_request_policy($1, $2, $3, $4, $5)")
        {
            Parameters =
            {
                Text(request), Text(requestedIsolation), Text(headers.ProtocolVersion), Text(headers.Method), Text(headers.Name),
            },
        }, async (reader, token) =>
        {
            await reader.ReadAsync(token);
            return new Policy(Nullable(reader, 0), reader.GetBoolean(1), reader.GetBoolean(2))
            {
                Handler = Nullable(reader, 3),
                Annotations = ParseAnnotations(Nullable(reader, 4)),
                Modern = reader.GetBoolean(5),
                Refusal = Nullable(reader, 6),
            };
        }, ct);

    public Task<string?> McpRequestAsync(RequestContext ctx, Policy policy, string request, string? callerContext,
                                         CancellationToken ct) =>
        RunAsync(ctx, policy, new NpgsqlBatchCommand("SELECT (api.mcp_handle_request($1::jsonb, $2::jsonb)).envelope::text")
        {
            Parameters = { Text(request), Text(callerContext) },
        }, async (reader, token) => await reader.ReadAsync(token) ? Nullable(reader, 0) : null, ct);

    // One transaction, in one round trip: SET TRANSACTION first (a dispatch
    // only; a policy call runs in the batch's implicit transaction), then
    // the request's deadline as the statement_timeout, then the call. The
    // commit is inside too, because it can fail with 40001 (SerializationRetry).
    //
    // The deadline bounds the call twice. On the server, statement_timeout
    // cancels a statement that would outrun it, so the backend stops working
    // and the connection goes back to the pool. Here, the linked token makes
    // Npgsql send a cancel request for what the server cannot end by itself
    // (a pool wait, a stalled network). Npgsql's own CommandTimeout (30 s by
    // default) is off: the request's deadline is the only clock.
    private async Task<T> RunAsync<T>(RequestContext ctx, Policy? dispatch, NpgsqlBatchCommand call,
                                      Func<NpgsqlDataReader, CancellationToken, Task<T>> read, CancellationToken ct)
    {
        var left = ctx.Remaining;
        var timeout = StatementTimeout(left);
        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(ct);
        deadline.CancelAfter(left);
        try
        {
            await using var connection = await OpenAsync(deadline.Token);
            await using var transaction = dispatch is null
                ? null
                : await connection.BeginTransactionAsync(
                    dispatch.Isolation is { } level ? Isolation[level] : IsolationLevel.Unspecified, deadline.Token);
            await using var batch = new NpgsqlBatch(connection, transaction) { Timeout = 0 };
            if (dispatch is not null)
            {
                batch.BatchCommands.Add(new NpgsqlBatchCommand("SET TRANSACTION " + (dispatch.ReadOnly ? "READ ONLY" : "READ WRITE") +
                                                               (dispatch.Deferrable ? " DEFERRABLE" : "")));
            }
            batch.BatchCommands.Add(new NpgsqlBatchCommand("SELECT set_config('statement_timeout', $1, true)")
            {
                Parameters = { Text(timeout) },
            });
            batch.BatchCommands.Add(call);
            T result;
            await using (var reader = await batch.ExecuteReaderAsync(deadline.Token))
            {
                await reader.NextResultAsync(deadline.Token);
                result = await read(reader, deadline.Token);
            }
            if (transaction is not null)
            {
                await transaction.CommitAsync(deadline.Token);
            }
            return result;
        }
        // Npgsql reports a cancellation it asked for as OperationCanceledException;
        // the server's own statement_timeout arrives as 57014. Either is the
        // deadline only when it has passed and the client is still there.
        catch (Exception e) when (!ct.IsCancellationRequested
                                  && (deadline.IsCancellationRequested || ctx.Remaining == TimeSpan.Zero)
                                  && e is OperationCanceledException or PostgresException { SqlState: PostgresErrorCodes.QueryCanceled })
        {
            throw new DeadlineExceededException(e);
        }
    }

    // What is left of the request, in whole milliseconds rounded up so the
    // server never cancels before the gateway would. Never "0": that is no
    // timeout at all.
    public static string StatementTimeout(TimeSpan remaining) =>
        remaining > TimeSpan.Zero
            ? ((long)Math.Ceiling(remaining.TotalMilliseconds)).ToString(System.Globalization.CultureInfo.InvariantCulture)
            : throw new DeadlineExceededException(null);

    // A refused or unreachable server is "no connection" as much as an
    // exhausted pool: 503, the request was never sent. A PostgresException
    // (a wrong password, say) is a server answer and stays a 500.
    private async Task<NpgsqlConnection> OpenAsync(CancellationToken ct)
    {
        try
        {
            return await dataSource.OpenConnectionAsync(ct);
        }
        catch (NpgsqlException e) when (e is not PostgresException && !ct.IsCancellationRequested)
        {
            throw new DatabaseUnavailableException(e);
        }
    }

    private static NpgsqlParameter Text(string? value) =>
        new() { Value = (object?)value ?? DBNull.Value, NpgsqlDbType = NpgsqlDbType.Text };

    private static string? Nullable(NpgsqlDataReader reader, int column) =>
        reader.IsDBNull(column) ? null : reader.GetString(column);

    private static IReadOnlyDictionary<string, JsonElement> ParseAnnotations(string? json)
    {
        if (json is null)
        {
            return Policy.NoAnnotations;
        }
        using var document = JsonDocument.Parse(json);
        return document.RootElement.EnumerateObject().ToDictionary(p => p.Name, p => p.Value.Clone());
    }

    public static string? NormalizeIsolation(string value)
    {
        var canonical = string.Join(' ', value.Trim().ToLowerInvariant()
            .Split([' ', '\t', '_', '-'], StringSplitOptions.RemoveEmptyEntries));
        return canonical == "read uncommitted" ? "read committed" : Isolation.ContainsKey(canonical) ? canonical : null;
    }
}
