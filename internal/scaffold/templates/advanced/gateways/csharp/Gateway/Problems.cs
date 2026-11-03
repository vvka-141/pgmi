// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// The problems the gateway answers itself: RFC 9457 problem details with a
// `code` clients branch on (../../PROTOCOL.md, "Status codes"). The database
// answers its own problems; these are only the ones raised before or around it.

using Microsoft.AspNetCore.Diagnostics;
using Microsoft.AspNetCore.Mvc;

namespace Pgmi.Gateway;

public static class Problems
{
    public static IResult Problem(int status, string title, string code, string? detail = null) =>
        TypedResults.Problem(detail, statusCode: status, title: title, type: "about:blank",
                             extensions: new Dictionary<string, object?> { ["code"] = code });

    public static IResult NotFound() => Problem(404, "Not Found", "pgmi.not_found");

    // Answers that carry a header: Retry-After, X-Request-Id.
    public static IResult WithHeader(this IResult result, HttpContext http, string name, string value)
    {
        http.Response.Headers[name] = value;
        return result;
    }

    // Every problem is uncacheable, whoever wrote it.
    public static void Customize(ProblemDetailsContext context)
    {
        context.ProblemDetails.Type = "about:blank";
        context.HttpContext.Response.Headers.CacheControl = "no-store";
    }
}

// The pool had no connection within GatewayOptions.PoolTimeout. 503 with
// Retry-After: the request was never sent, so repeating it is safe.
public sealed class DatabaseUnavailableException(Exception inner)
    : Exception("no database connection within the acquire timeout", inner);

// What nothing else answered. UseExceptionHandler has cleared whatever the
// response held already (a problem replaces headers copied from the
// database), so the client sees a coded problem and never an exception text.
public sealed class GatewayExceptionHandler(IProblemDetailsService problems, ILogger<GatewayExceptionHandler> logger)
    : IExceptionHandler
{
    public async ValueTask<bool> TryHandleAsync(HttpContext http, Exception exception, CancellationToken ct)
    {
        var (status, title, code, detail) = exception switch
        {
            BadHttpRequestException { StatusCode: StatusCodes.Status413PayloadTooLarge } =>
                (413, "Content Too Large", "pgmi.body_too_large", $"the limit is {GatewayOptions.MaxBodyBytes} bytes"),
            BadHttpRequestException => (400, "Bad Request", "pgmi.bad_request", null),
            DatabaseUnavailableException => (503, "Service Unavailable", "pgmi.unavailable", "no database connection is free; repeat the request"),
            _ => (500, "Internal Server Error", "pgmi.internal_error", null),
        };
        if (status == 500)
        {
            logger.LogError(exception, "{Method} {Path} failed", http.Request.Method, http.Request.Path);
        }
        else if (status == 503)
        {
            logger.LogWarning("no pooled connection within {Seconds} s", GatewayOptions.PoolTimeout.TotalSeconds);
            http.Response.Headers.RetryAfter = "1";
        }
        http.Response.StatusCode = status;
        return await problems.TryWriteAsync(new ProblemDetailsContext
        {
            HttpContext = http,
            Exception = exception,
            ProblemDetails = new ProblemDetails
            {
                Status = status, Title = title, Detail = detail, Extensions = { ["code"] = code },
            },
        });
    }
}
