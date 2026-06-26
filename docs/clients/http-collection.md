---
title: "HTTP collections"
description: "Generate importable HTTP request collections from an advanced-template OpenAPI contract."
weight: 70
---

# HTTP Collections

For ad-hoc API exploration without codegen, generate importable request files from `/openapi.json`.

## VS Code REST Client (.http files)

Generate `.http` files from the spec using `jq`:

```bash
curl -s http://localhost:3000/openapi.json | jq -r '
  .paths | to_entries[] | .key as $path |
  .value | to_entries[] |
  "\(.key | ascii_upcase) http://localhost:3000\($path)\n\n###\n"
' > api-requests.http
```

Open `api-requests.http` in VS Code with the REST Client extension. Click "Send Request" above any block.

For authenticated endpoints, add a header block:

```http
### Authenticated request
GET http://localhost:3000/me
x-user-id: google|your-subject-id

###
```

## Bruno

Import the OpenAPI spec directly into Bruno:

1. Open Bruno
2. **Collection** > **Import Collection**
3. Select **OpenAPI V3** as the format
4. Point at `http://localhost:3000/openapi.json` or paste the JSON

Bruno creates a request for each operation, organized by path.

## IntelliJ / JetBrains

JetBrains IDEs natively support `.http` files. Use the same `jq` recipe above, or import via:

1. **File** > **New** > **HTTP Request**
2. **Convert from** > **OpenAPI Specification**
3. Enter `http://localhost:3000/openapi.json`

## Insomnia / Postman

Both import OpenAPI specs directly:

1. **Import** > **From URL**
2. Enter `http://localhost:3000/openapi.json`

The collection auto-updates when you re-import.

## curl one-liner

Fetch the spec and list all endpoints:

```bash
curl -s http://localhost:3000/openapi.json | \
  jq -r '.paths | to_entries[] | .key as $p | .value | keys[] | "\(. | ascii_upcase) \($p)"'
```
