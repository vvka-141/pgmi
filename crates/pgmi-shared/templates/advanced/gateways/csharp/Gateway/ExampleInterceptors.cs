// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// The three example interceptors INTERCEPTORS can switch on (../../PROTOCOL.md,
// "The examples"): embed, request-id and header-names. README.md shows a real embedder
// behind IHttpClientFactory and a URL-signing response interceptor.

using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using Microsoft.Extensions.Options;

namespace Pgmi.Gateway;

// The outside call behind an interface: a real one posts to an embedding API
// through a typed HttpClient; tests and the harness use the fake.
public interface IEmbedder
{
    Task<IReadOnlyList<double[]>> EmbedAsync(IReadOnlyList<string> texts, CancellationToken ct);
}

// Deterministic and offline, the same in every reference gateway: the first
// four bytes of the SHA-256 of the UTF-8 text, each divided by 255, rounded
// to four places. Like a real model with a token limit, it refuses a text
// over MaxChars.
public sealed class FakeEmbedder : IEmbedder
{
    public const int MaxChars = 2000;

    public Task<IReadOnlyList<double[]>> EmbedAsync(IReadOnlyList<string> texts, CancellationToken ct)
    {
        var vectors = new List<double[]>(texts.Count);
        foreach (var text in texts)
        {
            if (text.Length > MaxChars)
            {
                throw new ArgumentException($"text over {MaxChars} characters");
            }
            // decimal, not double: Math.Round on a double scales by 10^4 and
            // can round differently from the decimal rounding the other
            // languages do; the vectors must match byte for byte.
            vectors.Add(SHA256.HashData(Encoding.UTF8.GetBytes(text))[..4]
                .Select(b => (double)Math.Round(b / 255m, 4, MidpointRounding.ToEven)).ToArray());
        }
        return Task.FromResult<IReadOnlyList<double[]>>(vectors);
    }
}

// For each inputSchema property marked "x-embed": true, adds a
// <property>_embedding sibling holding its vector. The schema declares the
// sibling too, so the database validates it like any other field. One
// batched call for all fields. A sibling already present is left alone: a
// client that embeds for itself is not embedded twice.
//
// ADAPT: an embedding outage can degrade (the handler searches without the
// vector) or fail (EMBED_FAILURE_POLICY); choose per project.
public sealed class EmbedFields(IEmbedder embedder, IOptions<GatewayOptions> options) : IRequestInterceptor
{
    public string Name => "embed";
    public bool NeedsJson => true;
    public FailurePolicy Failure { get; } = options.Value.EmbedFailurePolicy == "degrade" ? FailurePolicy.Degrade : FailurePolicy.Fail;
    public TimeSpan Timeout => TimeSpan.FromSeconds(2);

    public bool AppliesTo(RequestContext ctx) => ctx.Annotations.Values.Any(IsEmbedded);

    public async ValueTask OnRequestAsync(InterceptedRequest request, RequestContext ctx, CancellationToken ct)
    {
        if (request.Arguments(ctx) is not { } args)
        {
            return;
        }
        var fields = ctx.Annotations
            .Where(a => IsEmbedded(a.Value)
                        && args[a.Key] is JsonValue value && value.TryGetValue<string>(out var text) && !string.IsNullOrWhiteSpace(text)
                        && !args.ContainsKey(a.Key + "_embedding"))
            .Select(a => a.Key)
            .ToList();
        if (fields.Count == 0)
        {
            return;
        }
        var vectors = await embedder.EmbedAsync(fields.Select(f => args[f]!.GetValue<string>()).ToList(), ct);
        foreach (var (field, vector) in fields.Zip(vectors))
        {
            args[field + "_embedding"] = new JsonArray(vector.Select(v => (JsonNode)v).ToArray());
        }
    }

    private static bool IsEmbedded(JsonElement annotation) =>
        annotation.ValueKind == JsonValueKind.Object
        && annotation.TryGetProperty("x-embed", out var flag) && flag.ValueKind == JsonValueKind.True;
}

// Adds headerNames, the sorted names of the request's headers, to the
// arguments. Names, never values: a value can be a credential.
public sealed class HeaderNames : IRequestInterceptor
{
    public string Name => "header-names";
    public bool NeedsJson => true;
    public FailurePolicy Failure => FailurePolicy.Fail;
    public TimeSpan Timeout => TimeSpan.FromSeconds(1);

    public bool AppliesTo(RequestContext ctx) => true;

    public ValueTask OnRequestAsync(InterceptedRequest request, RequestContext ctx, CancellationToken ct)
    {
        if (request.Arguments(ctx) is { } args)
        {
            args["headerNames"] = new JsonArray(request.Headers.Keys.Order(StringComparer.Ordinal).Select(n => (JsonNode)n).ToArray());
        }
        return ValueTask.CompletedTask;
    }
}

// Answers the gateway's request id in X-Request-Id, the id its log lines
// carry. Headers only: the body is never parsed.
public sealed class RequestIdHeader : IResponseInterceptor
{
    public string Name => "request-id";
    public bool NeedsJson => false;
    public FailurePolicy Failure => FailurePolicy.Fail;
    public TimeSpan Timeout => TimeSpan.FromSeconds(1);

    public bool AppliesTo(RequestContext ctx) => true;

    public ValueTask OnResponseAsync(InterceptedResponse response, RequestContext ctx, CancellationToken ct)
    {
        response.Headers["x-request-id"] = ctx.RequestId;
        return ValueTask.CompletedTask;
    }
}
