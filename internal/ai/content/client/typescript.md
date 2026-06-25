# pgmi API Client — TypeScript

**Recommended generator:** `openapi-typescript` + `openapi-fetch`

```bash
npx openapi-typescript http://localhost:8080/openapi.json -o src/api/schema.d.ts
```

## Transport Core Skeleton

```typescript
import createClient from "openapi-fetch";
import type { paths } from "./schema";

const client = createClient<paths>({
  baseUrl: process.env.API_BASE_URL ?? "http://localhost:8080",
  headers: {
    Authorization: `Bearer ${token}`,
  },
});

// Error handling — RFC 9457 Problem Details
interface ProblemDetails {
  type: string;
  title: string;
  status: number;
  detail?: string;
}

async function unwrap<T>(response: { data?: T; error?: ProblemDetails }) {
  if (response.error) {
    throw new Error(`${response.error.status} ${response.error.title}: ${response.error.detail}`);
  }
  return response.data as T;
}
```

## Anti-Copy Directive

**DO NOT copy this skeleton verbatim.** It demonstrates auth + error handling only. Generate your endpoint methods from the live `/openapi.json` spec using `openapi-typescript`, then call them through the typed `client` above. Do not hand-write endpoint URLs or request shapes from memory.
