---
title: "Generic (any language)"
weight: 60
---

# Generic Client Generation (Any Language)

The OpenAPI Generator project supports 50+ languages and frameworks. Use it when no language-specific tool is a better fit.

## openapi-generator

```bash
# Install via npm, Homebrew, or Docker
npm install -g @openapitools/openapi-generator-cli

# List available generators
openapi-generator-cli list

# Generate a client
openapi-generator-cli generate \
  -i http://localhost:3000/openapi.json \
  -g <generator-name> \
  -o ./generated-client
```

Common generators:

| Generator | Language/Framework |
|-----------|-------------------|
| `typescript-fetch` | TypeScript with Fetch API |
| `typescript-axios` | TypeScript with Axios |
| `go` | Go |
| `python` | Python |
| `csharp` | C# |
| `java` | Java |
| `kotlin` | Kotlin |
| `rust` | Rust |
| `swift5` | Swift |
| `dart` | Dart/Flutter |
| `ruby` | Ruby |
| `php` | PHP |

## Docker (no local install)

```bash
docker run --rm --network host \
  -v "${PWD}:/out" \
  openapitools/openapi-generator-cli generate \
  -i http://host.docker.internal:3000/openapi.json \
  -g typescript-fetch \
  -o /out/generated
```

## Configuration

Customize generation with a config file:

```yaml
# openapi-generator-config.yaml
generatorName: typescript-fetch
inputSpec: http://localhost:3000/openapi.json
outputDir: ./generated
additionalProperties:
  supportsES6: true
  withInterfaces: true
```

```bash
openapi-generator-cli batch openapi-generator-config.yaml
```
