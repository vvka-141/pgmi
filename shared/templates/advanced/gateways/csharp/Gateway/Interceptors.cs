// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// Request and response interceptors: an ordered pipeline around each dispatch
// (../../PROTOCOL.md, "Interceptors"). A request interceptor runs after the
// policy call and before the transaction opens; it may change the method,
// URL, headers and body. A response interceptor runs after the commit; it may
// change the status, headers and body. Uses: embedding vectors, enrichment
// from another service, signing storage URLs the handler returned, redaction.
//
// An interceptor sees every header the client sent, credentials included
// (Authorization, Cookie, X-Connection), so it can exchange or sign them for
// another service. It must never log, store or forward them except to the
// service the caller authorised. Identity is the database's: on /api the
// gateway drops the client's handles and sets its own after the pipeline,
// and on /mcp only the body reaches the database.
//
// The rules:
// - The database still decides. An interceptor never decides access or
//   validity; it selects itself from the RequestContext (the handler name,
//   the x-* annotations of its inputSchema), never from a list of paths.
// - Zero cost when unused. No interceptor applies: the body passes as the
//   client's bytes, never parsed. Some need JSON: parsed once, serialized once.
// - Failure policy per interceptor. Fail answers 502 pgmi.interceptor_failed;
//   Degrade logs and goes on with the message as it was before. A timeout
//   (bounded by the request's deadline) is a failure.
// - No database connection is held while an interceptor runs.
// - Thread safety. Interceptors are singletons that keep no per-request
//   state: it travels in the message and the context. Anything long-lived
//   they use (an HttpClient from IHttpClientFactory, a signing key) is
//   injected at startup.
//
// They are DI services keyed by name (Program.cs); INTERCEPTORS picks which
// run, in its order. ExampleInterceptors.cs has the three examples.

using System.Text.Encodings.Web;
using System.Text.Json;
using System.Text.Json.Nodes;

namespace Pgmi.Gateway;

public enum FailurePolicy { Fail, Degrade }

public interface IInterceptor
{
    string Name { get; }

    // Reads or writes message.Json; one that only touches headers says false
    // and the body is never parsed for it.
    bool NeedsJson { get; }

    FailurePolicy Failure { get; }

    TimeSpan Timeout { get; }

    bool AppliesTo(RequestContext ctx);
}

public interface IRequestInterceptor : IInterceptor
{
    ValueTask OnRequestAsync(InterceptedRequest request, RequestContext ctx, CancellationToken ct);
}

public interface IResponseInterceptor : IInterceptor
{
    ValueTask OnResponseAsync(InterceptedResponse response, RequestContext ctx, CancellationToken ct);
}

// Headers and a body, readable as bytes or as JSON. Json parses on first read
// and keeps the result, so every interceptor shares one parse; Finish()
// serializes once. A body that is not JSON reads as null and passes on as the
// bytes it was: the database answers it.
public abstract class InterceptedMessage
{
    private static readonly JsonSerializerOptions Compact = new() { Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping };
    private JsonNode? _json;

    public Dictionary<string, string> Headers { get; private set; } = new(StringComparer.OrdinalIgnoreCase);
    public byte[]? Body { get; set; }
    public bool Parsed { get; private set; }

    public JsonNode? Json
    {
        get
        {
            if (!Parsed)
            {
                Parsed = true;
                try
                {
                    _json = Body is { Length: > 0 } ? JsonNode.Parse(Body) : null;
                }
                catch (JsonException)
                {
                    _json = null;
                }
            }
            return _json;
        }
        set => (_json, Parsed) = (value, true);
    }

    protected void Init(IEnumerable<KeyValuePair<string, string>> headers, byte[]? body, JsonNode? parsed)
    {
        Headers = new Dictionary<string, string>(headers, StringComparer.OrdinalIgnoreCase);
        Body = body;
        if (parsed is not null)
        {
            Json = parsed;
        }
    }

    public void Finish()
    {
        if (Parsed && _json is not null)
        {
            Body = JsonSerializer.SerializeToUtf8Bytes(_json, Compact);
        }
    }

    // Degrade must be able to go on "as before": the interceptor works on a
    // copy, adopted only when it succeeds.
    public abstract InterceptedMessage Copy();

    public abstract void Adopt(InterceptedMessage other);

    protected void CopyInto(InterceptedMessage twin)
    {
        twin.Headers = new Dictionary<string, string>(Headers, StringComparer.OrdinalIgnoreCase);
        twin.Body = Body;
        twin._json = _json?.DeepClone();
        twin.Parsed = Parsed;
    }

    protected void AdoptFrom(InterceptedMessage other)
    {
        (Headers, Body, _json, Parsed) = (other.Headers, other.Body, other._json, other.Parsed);
    }
}

public sealed class InterceptedRequest : InterceptedMessage
{
    public InterceptedRequest(string method, string url, IEnumerable<KeyValuePair<string, string>> headers, byte[]? body,
                              JsonNode? parsed = null)
    {
        (Method, Url) = (method, url);
        Init(headers, body, parsed);
    }

    public string Method { get; set; }
    public string Url { get; set; }

    // The object the handler's inputSchema describes: the REST body, or an
    // MCP call's params.arguments. Null when there is none.
    public JsonObject? Arguments(RequestContext ctx) =>
        (ctx.Protocol == "mcp" ? Json?["params"]?["arguments"] : Json) as JsonObject;

    public override InterceptedMessage Copy()
    {
        var twin = new InterceptedRequest(Method, Url, [], null);
        CopyInto(twin);
        return twin;
    }

    public override void Adopt(InterceptedMessage other)
    {
        AdoptFrom(other);
        (Method, Url) = (((InterceptedRequest)other).Method, ((InterceptedRequest)other).Url);
    }
}

public sealed class InterceptedResponse : InterceptedMessage
{
    public InterceptedResponse(int status, IEnumerable<KeyValuePair<string, string>> headers, byte[]? body, JsonNode? parsed = null)
    {
        Status = status;
        Init(headers, body, parsed);
    }

    public int Status { get; set; }

    public override InterceptedMessage Copy()
    {
        var twin = new InterceptedResponse(Status, [], null);
        CopyInto(twin);
        return twin;
    }

    public override void Adopt(InterceptedMessage other)
    {
        AdoptFrom(other);
        Status = ((InterceptedResponse)other).Status;
    }
}

public sealed class InterceptorFailedException(string name, Exception inner) : Exception($"interceptor {name} failed", inner)
{
    public string Name { get; } = name;
}

// Built once at startup from the keyed services INTERCEPTORS names, read-only
// afterwards.
public sealed class InterceptorPipeline(IReadOnlyList<IRequestInterceptor> requests, IReadOnlyList<IResponseInterceptor> responses,
                                        ILogger<InterceptorPipeline> logger)
{
    public IReadOnlyList<IRequestInterceptor> RequestsFor(RequestContext ctx) =>
        requests.Count == 0 ? [] : requests.Where(i => i.AppliesTo(ctx)).ToList();

    public IReadOnlyList<IResponseInterceptor> ResponsesFor(RequestContext ctx) =>
        responses.Count == 0 ? [] : responses.Where(i => i.AppliesTo(ctx)).ToList();

    public Task RunAsync(IReadOnlyList<IRequestInterceptor> selected, InterceptedRequest request, RequestContext ctx,
                         CancellationToken ct) =>
        RunAsync(selected, request, ctx, (i, m, c, t) => i.OnRequestAsync((InterceptedRequest)m, c, t), ct);

    public Task RunAsync(IReadOnlyList<IResponseInterceptor> selected, InterceptedResponse response, RequestContext ctx,
                         CancellationToken ct) =>
        RunAsync(selected, response, ctx, (i, m, c, t) => i.OnResponseAsync((InterceptedResponse)m, c, t), ct);

    private async Task RunAsync<TInterceptor>(IReadOnlyList<TInterceptor> selected, InterceptedMessage message, RequestContext ctx,
                                              Func<TInterceptor, InterceptedMessage, RequestContext, CancellationToken, ValueTask> hook,
                                              CancellationToken ct)
        where TInterceptor : IInterceptor
    {
        foreach (var interceptor in selected)
        {
            var work = interceptor.Failure == FailurePolicy.Degrade ? message.Copy() : message;
            if (interceptor.NeedsJson)
            {
                _ = work.Json;
            }
            var limit = interceptor.Timeout < ctx.Remaining ? interceptor.Timeout : ctx.Remaining;
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
            timeout.CancelAfter(limit);
            try
            {
                // WaitAsync bounds an interceptor that ignores its token: it is
                // abandoned, and a degraded one's copy is never read.
                await hook(interceptor, work, ctx with { Deadline = DateTimeOffset.UtcNow + limit }, timeout.Token)
                    .AsTask().WaitAsync(limit, ct);
            }
            catch (Exception e) when (!ct.IsCancellationRequested)
            {
                var reason = e is TimeoutException or OperationCanceledException ? "timed out" : $"{e.GetType().Name}: {e.Message}";
                if (interceptor.Failure == FailurePolicy.Degrade)
                {
                    logger.LogWarning("{RequestId} {Interceptor} degraded ({Reason})", ctx.RequestId, interceptor.Name, reason);
                    continue;
                }
                logger.LogError("{RequestId} {Interceptor} failed ({Reason})", ctx.RequestId, interceptor.Name, reason);
                throw new InterceptorFailedException(interceptor.Name, e);
            }
            if (!ReferenceEquals(work, message))
            {
                message.Adopt(work);
            }
        }
        message.Finish();
    }
}
