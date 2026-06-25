# Client Generation

The advanced template serves an OpenAPI 3.1 specification at `GET /openapi.json` and an interactive explorer at `GET /docs`. Use these to generate typed clients in any language.

pgmi does not ship client libraries. Your deployment owns the spec; your pipeline owns the client.

## Quick Start

1. Deploy your project with pgmi
2. Open `http://localhost:3000/docs` for the interactive API explorer
3. Pick a language below and generate a typed client

## Language Recipes

| Language | Tool | Recipe |
|----------|------|--------|
| TypeScript | openapi-typescript | [typescript.md](typescript.md) |
| Go | oapi-codegen | [go.md](go.md) |
| Python | openapi-python-client | [python.md](python.md) |
| C# | NSwag | [csharp.md](csharp.md) |
| Any | openapi-generator | [generic.md](generic.md) |

## HTTP Collections

For ad-hoc exploration without codegen, see [http-collection.md](http-collection.md) to generate importable request files for Bruno, VS Code REST Client, or IntelliJ.

## Philosophy

These recipes teach the approach, not pinned versions. The spec is the source of truth; the generated client is a downstream artifact in your CI pipeline. When your handlers change, re-run the generator.
