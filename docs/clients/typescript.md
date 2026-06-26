---
title: "TypeScript"
weight: 20
---

# TypeScript Client Generation

Generate TypeScript types and a fetch-based client from your deployment's `/openapi.json`.

## openapi-typescript (types only)

Generates TypeScript types from the spec. Pair with `openapi-fetch` for a typed fetch wrapper.

```bash
npx openapi-typescript http://localhost:3000/openapi.json -o src/api.d.ts
```

Use the types with `openapi-fetch`:

```typescript
import createClient from "openapi-fetch";
import type { paths } from "./api";

const client = createClient<paths>({ baseUrl: "http://localhost:3000" });

const { data, error } = await client.GET("/hello", {
  params: { query: { name: "World" } },
});
```

## openapi-generator (full client)

Generates a complete client with models and API classes:

```bash
npx @openapitools/openapi-generator-cli generate \
  -i http://localhost:3000/openapi.json \
  -g typescript-fetch \
  -o src/generated
```

## CI Integration

Add to your build pipeline so the client stays in sync with the spec:

```yaml
# GitHub Actions example
- run: npx openapi-typescript $API_URL/openapi.json -o src/api.d.ts
  env:
    API_URL: ${{ vars.API_URL }}
```
