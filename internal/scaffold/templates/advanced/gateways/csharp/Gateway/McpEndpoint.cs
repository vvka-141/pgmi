// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// POST /mcp: MCP over Streamable HTTP, JSON answers only, both protocol eras
// (../../PROTOCOL.md, "MCP"). The gateway knows no protocol version: it
// forwards the body and the MCP-Protocol-Version, Mcp-Method and Mcp-Name
// headers to api.mcp_request_policy, whose refusal is the whole answer. The
// status of a dispatched answer follows PROTOCOL.md's table, the one piece of
// protocol knowledge kept here. Mcp-Session-Id and Last-Event-ID are ignored:
// there is no session.
//
// The per-address rate limit and the Origin check are endpoint filters
// (Program.cs). What the gateway refuses itself is a problem, never an
// empty body. This is the rest:
//
//     media types -> caller (token or handle) -> parse -> policy
//       -> request interceptors -> dispatch -> response interceptors

using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;

namespace Pgmi.Gateway;

public sealed class McpEndpoint
{
    public static async Task<IResult> HandleAsync(HttpContext http, IDbChannel channel, InterceptorPipeline pipeline,
                                                  ResourceServer resourceServer, McpRateLimits limits, ILogger<McpEndpoint> logger)
    {
        var request = http.Request;
        var ct = http.RequestAborted;
        if (!string.Equals(request.GetTypedHeaders().ContentType?.MediaType.Value, "application/json", StringComparison.OrdinalIgnoreCase))
        {
            return Problems.Problem(415, "Unsupported Media Type", "pgmi.unsupported_media_type",
                                    "MCP requires Content-Type: application/json");
        }
        if (request.Headers.Accept is { Count: > 0 } accept && !AcceptsJson(accept.ToString()))
        {
            return Problems.Problem(406, "Not Acceptable", "pgmi.not_acceptable", "this endpoint answers application/json");
        }
        if (request.Headers["MCP-Protocol-Version"] is { Count: > 0 } version)
        {
            http.Response.Headers["MCP-Protocol-Version"] = version;
        }

        // Who calls. A connection handle comes as Authorization: Bearer cn_...
        // or, with no other Authorization, as X-Connection, and goes on as it
        // is: the database resolves it. With MCP_RESOURCE set any other bearer
        // token is verified here and only the identity it proves goes on; a
        // request with neither is 401, so a client learns where to get one.
        string? caller;
        var handle = BearerHandle(request.Headers.Authorization.ToString());
        if (handle is null && (!resourceServer.Enabled || request.Headers.Authorization.Count == 0)
            && request.Headers["X-Connection"].ToString() is { Length: > 0 } sent)
        {
            handle = sent;
        }
        if (handle is not null || !resourceServer.Enabled)
        {
            caller = handle is not null ? JsonSerializer.Serialize(new { connection = handle }) : null;
        }
        else
        {
            VerifiedClient client;
            try
            {
                client = await resourceServer.VerifyAsync(request.Headers.Authorization.Count > 0 ? request.Headers.Authorization.ToString() : null, ct);
            }
            catch (TokenRejectedException e)
            {
                // The reason is logged, the token never is.
                logger.LogInformation("token refused: {Reason}", e.Message);
                http.Response.Headers.WWWAuthenticate = resourceServer.Challenge(e.Error);
                return Results.StatusCode(StatusCodes.Status401Unauthorized);
            }
            catch (IdentityProviderUnavailableException e)
            {
                logger.LogWarning("identity provider unavailable: {Reason}", e.Message);
                return Error(http, 503, null, -31000, "The identity provider is unavailable; repeat the call later",
                             new JsonObject { ["code"] = "pgmi.identity_provider_unavailable" }, retryAfter: "5");
            }
            var (allowed, retryAfter) = limits.PerClient.TryTake(client.RateLimitKey);
            if (!allowed)
            {
                return McpRateLimits.TooMany(http, retryAfter);
            }
            caller = client.Context;
        }

        byte[] raw;
        using (var buffer = new MemoryStream())
        {
            await request.Body.CopyToAsync(buffer, ct);
            raw = buffer.ToArray();
        }
        JsonNode? message;
        try
        {
            message = JsonNode.Parse(raw);
        }
        catch (JsonException)
        {
            return Error(http, 400, null, -32700, "Parse error");
        }
        var id = RequestId(message);
        var text = Encoding.UTF8.GetString(raw);

        string? requested = null;
        if (request.Headers["X-PGMI-Transaction-Isolation"] is { Count: > 0 } isolation
            && (requested = PostgresChannel.NormalizeIsolation(isolation.ToString())) is null)
        {
            return Error(http, 400, id, -32600, $"Unsupported X-PGMI-Transaction-Isolation: {isolation}",
                         new JsonObject { ["code"] = "pgmi.transaction_isolation_unsupported" });
        }

        var mcpHeaders = new McpHeaders(Header(request, "MCP-Protocol-Version"), Header(request, "Mcp-Method"), Header(request, "Mcp-Name"));
        var ctx = RequestContext.New("mcp", (message as JsonObject)?["method"]?.ToString() ?? "", "/mcp");
        try
        {
            // One call before the transaction: the protocol checks (the
            // database owns them), the transaction policy and the handler's
            // metadata. A refusal is the answer and nothing is dispatched,
            // so nothing is logged.
            var policy = await channel.McpPolicyAsync(ctx, text, requested, mcpHeaders, ct);
            if (policy.Refusal is null)
            {
                ctx = ctx.WithPolicy(policy);
                if (pipeline.RequestsFor(ctx) is { Count: > 0 } before)
                {
                    // Already parsed: the interceptors share this parse. On
                    // /mcp only the body reaches the database.
                    var route = Route(message);
                    var intercepted = new InterceptedRequest("POST", "/mcp", request.Headers.ToDictionary(h => h.Key.ToLowerInvariant(), h => h.Value.ToString()),
                                                             null, message);
                    await pipeline.RunAsync(before, intercepted, ctx, ct);
                    message = intercepted.Json;
                    text = intercepted.Body is { } changed ? Encoding.UTF8.GetString(changed) : text;
                    if (Route(message) != route)
                    {
                        policy = await channel.McpPolicyAsync(ctx, text, requested, mcpHeaders, ct);
                        if (policy.Refusal is null)
                        {
                            ctx = ctx.WithPolicy(policy);
                        }
                    }
                }
            }
            if (policy.Refusal is not null)
            {
                return Answer(http, 400, policy.Refusal);
            }

            var envelope = await channel.McpRequestAsync(ctx, policy, text, caller, ct);
            if (envelope is null)
            {
                // A notification: accepted, nothing to answer.
                return Results.StatusCode(StatusCodes.Status202Accepted);
            }
            if (HandleUnknown(envelope))
            {
                http.Response.Headers.WWWAuthenticate = resourceServer.Challenge("invalid_token");
                return Results.StatusCode(StatusCodes.Status401Unauthorized);
            }
            var status = EnvelopeStatus(envelope, policy.Modern);
            if (pipeline.ResponsesFor(ctx) is { Count: > 0 } after)
            {
                var intercepted = new InterceptedResponse(status, [], Encoding.UTF8.GetBytes(envelope));
                await pipeline.RunAsync(after, intercepted, ctx, ct);
                foreach (var (name, value) in intercepted.Headers)
                {
                    http.Response.Headers[name] = value;
                }
                (status, envelope) = (intercepted.Status, Encoding.UTF8.GetString(intercepted.Body ?? []));
            }
            return Answer(http, status, envelope);
        }
        catch (RetriesExhaustedException e)
        {
            // The transaction lost a race every time it ran: nothing was
            // applied, and the same call may succeed when repeated.
            logger.LogWarning("{RequestId} serialization failure, retries exhausted ({Attempts}): {SqlState}",
                              ctx.RequestId, e.Attempts, e.SqlState);
            return Error(http, 200, id, -31000, "The call conflicted with a concurrent change and was rolled back; repeat it unchanged",
                         new JsonObject { ["code"] = "pgmi.transaction_retryable", ["sqlstate"] = e.SqlState, ["attempts"] = e.Attempts },
                         retryAfter: "1");
        }
        catch (InterceptorFailedException)
        {
            return Error(http, 502, id, -32603, "The gateway could not complete the call",
                         new JsonObject { ["code"] = "pgmi.interceptor_failed", ["requestId"] = ctx.RequestId });
        }
        catch (DatabaseUnavailableException)
        {
            logger.LogWarning("{RequestId} no pooled connection within {Seconds} s", ctx.RequestId, GatewayOptions.PoolTimeout.TotalSeconds);
            return Error(http, 503, id, -31000, "The database is unavailable; repeat the call later",
                         new JsonObject { ["code"] = "pgmi.unavailable" }, retryAfter: "1");
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            // Logged here; the client gets a sanitized error, never the text.
            logger.LogError(e, "{RequestId} dispatch error", ctx.RequestId);
            return Error(http, 500, id, -32603, "Internal error");
        }
    }

    // GET (no SSE stream to open), DELETE (no session to end), anything else.
    public static IResult MethodNotAllowed(HttpContext http)
    {
        http.Response.Headers.Allow = "POST";
        return Problems.Problem(405, "Method Not Allowed", "pgmi.method_not_allowed");
    }

    // The HTTP status of a dispatched answer (../../PROTOCOL.md, "MCP status
    // codes"): in the 2026-07-28 era -32601 is 404 and -32022 is 400; every
    // other answer is 200, a tool's -32602 included, and every legacy one.
    public static int EnvelopeStatus(string envelope, bool modern)
    {
        if (!modern)
        {
            return 200;
        }
        using var document = JsonDocument.Parse(envelope);
        if (!document.RootElement.TryGetProperty("error", out var error) || error.ValueKind != JsonValueKind.Object
            || !error.TryGetProperty("code", out var code) || !code.TryGetInt32(out var value))
        {
            return 200;
        }
        return value switch { -32601 => 404, -32022 => 400, _ => 200 };
    }

    // A connection handle sent as Authorization: Bearer cn_..., or null. A
    // cn_ handle is not an OAuth token and never reaches the verifier.
    public static string? BearerHandle(string authorization)
    {
        var parts = authorization.Split(' ', 2);
        var credentials = parts.Length == 2 ? parts[1].Trim() : "";
        return parts[0].Equals("Bearer", StringComparison.OrdinalIgnoreCase) && credentials.StartsWith("cn_", StringComparison.Ordinal)
            ? credentials
            : null;
    }

    // The database knows no live connection by the handle sent: answered
    // 401, as a token the gateway refused.
    public static bool HandleUnknown(string envelope)
    {
        using var document = JsonDocument.Parse(envelope);
        return document.RootElement.TryGetProperty("error", out var error) && error.ValueKind == JsonValueKind.Object
            && error.TryGetProperty("code", out var code) && code.TryGetInt32(out var value) && value == -31001
            && error.TryGetProperty("data", out var data) && data.ValueKind == JsonValueKind.Object
            && data.TryGetProperty("code", out var reason) && reason.ValueKind == JsonValueKind.String
            && reason.GetString() == "pgmi.connection_unknown";
    }

    // True if an Accept header admits application/json or a wildcard. A
    // media range with q=0 is a refusal (RFC 9110 §12.5.1), not an acceptance.
    public static bool AcceptsJson(string accept)
    {
        foreach (var entry in accept.Split(','))
        {
            var parts = entry.Split(';');
            if (parts[0].Trim().ToLowerInvariant() is not ("application/json" or "application/*" or "*/*"))
            {
                continue;
            }
            var q = parts.Skip(1).Select(p => p.Split('=', 2)).FirstOrDefault(p => p[0].Trim() == "q");
            if (q is null || q.Length < 2 || !double.TryParse(q[1].Trim(), System.Globalization.CultureInfo.InvariantCulture, out var weight) || weight > 0)
            {
                return true;
            }
        }
        return false;
    }

    // The JSON-RPC id of a request object: a string or an integer, else null.
    private static JsonNode? RequestId(JsonNode? message) =>
        (message as JsonObject)?["id"] is JsonValue id
        && (id.GetValueKind() == JsonValueKind.String || (id.GetValueKind() == JsonValueKind.Number && id.TryGetValue<long>(out _)))
            ? id.DeepClone()
            : null;

    // What api.mcp_request_policy routes on: an interceptor that changes it
    // sends the request to another handler, so the policy is resolved again.
    private static string Route(JsonNode? message)
    {
        var parameters = (message as JsonObject)?["params"] as JsonObject;
        return $"{(message as JsonObject)?["method"]?.ToJsonString()}|{parameters?["name"]?.ToJsonString()}|{parameters?["uri"]?.ToJsonString()}";
    }

    private static string? Header(HttpRequest request, string name) =>
        request.Headers[name] is { Count: > 0 } value ? value.ToString() : null;

    private static IResult Answer(HttpContext http, int status, string json) =>
        Results.Text(json, "application/json", Encoding.UTF8, status);

    private static IResult Error(HttpContext http, int status, JsonNode? id, int code, string message, JsonObject? data = null,
                                 string? retryAfter = null)
    {
        if (retryAfter is not null)
        {
            http.Response.Headers.RetryAfter = retryAfter;
        }
        var error = new JsonObject { ["code"] = code, ["message"] = message };
        if (data is not null)
        {
            error["data"] = data;
        }
        return Answer(http, status, new JsonObject { ["jsonrpc"] = "2.0", ["id"] = id, ["error"] = error }.ToJsonString());
    }
}
