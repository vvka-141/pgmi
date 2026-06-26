---
title: "C#"
weight: 50
---

# C# Client Generation

Generate a typed C# client from your deployment's `/openapi.json`.

## NSwag

NSwag generates C# clients with full async support. Install via dotnet tool or NuGet.

```bash
dotnet tool install -g NSwag.ConsoleCore

# Generate client
nswag openapi2csclient \
  /input:http://localhost:3000/openapi.json \
  /output:ApiClient.cs \
  /namespace:MyProject.Api \
  /generateClientClasses:true \
  /generateDtoTypes:true
```

Usage:

```csharp
using var httpClient = new HttpClient { BaseAddress = new Uri("http://localhost:3000") };
var client = new ApiClient(httpClient);

var result = await client.HelloWorldAsync("World");
Console.WriteLine(result.Message);
```

## MSBuild Integration

Add to your `.csproj` for build-time generation:

```xml
<Target Name="GenerateApiClient" BeforeTargets="CoreCompile">
  <Exec Command="nswag openapi2csclient /input:$(ApiUrl)/openapi.json /output:$(IntermediateOutputPath)ApiClient.cs /namespace:$(RootNamespace).Api" />
  <ItemGroup>
    <Compile Include="$(IntermediateOutputPath)ApiClient.cs" />
  </ItemGroup>
</Target>
```

## Alternative: Kiota

Microsoft's Kiota generates idiomatic clients for multiple languages:

```bash
dotnet tool install -g Microsoft.OpenApi.Kiota
kiota generate -l CSharp -d http://localhost:3000/openapi.json -o ./ApiClient -n MyProject.Api
```
