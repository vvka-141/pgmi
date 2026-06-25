# pgmi API Client — C#

**Recommended generator:** `NSwag`

```bash
nswag openapi2csclient /input:http://localhost:8080/openapi.json /output:PgmiClient.cs /namespace:Pgmi.Api
```

## Transport Core Skeleton

```csharp
using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text.Json.Serialization;

public class PgmiClient
{
    private readonly HttpClient _http;

    public PgmiClient(string baseUrl, string token)
    {
        _http = new HttpClient { BaseAddress = new Uri(baseUrl) };
        _http.DefaultRequestHeaders.Authorization =
            new AuthenticationHeaderValue("Bearer", token);
        _http.DefaultRequestHeaders.Accept.Add(
            new MediaTypeWithQualityHeaderValue("application/json"));
    }

    private async Task<T> RequestAsync<T>(HttpRequestMessage request)
    {
        var response = await _http.SendAsync(request);
        if (!response.IsSuccessStatusCode)
        {
            var problem = await response.Content.ReadFromJsonAsync<ProblemDetails>();
            throw new PgmiApiException(problem ?? new ProblemDetails
            {
                Status = (int)response.StatusCode,
                Title = response.ReasonPhrase ?? "Unknown"
            });
        }
        return (await response.Content.ReadFromJsonAsync<T>())!;
    }
}

public record ProblemDetails
{
    [JsonPropertyName("type")]   public string Type { get; init; } = "about:blank";
    [JsonPropertyName("title")]  public string Title { get; init; } = "";
    [JsonPropertyName("status")] public int Status { get; init; }
    [JsonPropertyName("detail")] public string? Detail { get; init; }
}

public class PgmiApiException(ProblemDetails problem) : Exception(
    $"{problem.Status} {problem.Title}: {problem.Detail}")
{
    public ProblemDetails Problem { get; } = problem;
}
```

## Anti-Copy Directive

**DO NOT copy this skeleton verbatim.** It demonstrates auth + error handling only. Generate your endpoint methods from the live `/openapi.json` spec using NSwag, then call them through the `RequestAsync` method above. Do not hand-write endpoint URLs or request shapes from memory.
