# pgmi API Client — Python

**Recommended generator:** `openapi-python-client`

```bash
openapi-python-client generate --url http://localhost:8080/openapi.json
```

## Transport Core Skeleton

```python
import httpx

class PgmiClient:
    def __init__(self, base_url: str, token: str):
        self._client = httpx.Client(
            base_url=base_url,
            headers={"Authorization": f"Bearer {token}"},
        )

    def _request(self, method: str, path: str, **kwargs):
        response = self._client.request(method, path, **kwargs)
        if response.status_code >= 400:
            problem = response.json()
            raise PgmiApiError(
                status=problem.get("status", response.status_code),
                title=problem.get("title", "Unknown"),
                detail=problem.get("detail"),
            )
        return response.json()


class PgmiApiError(Exception):
    def __init__(self, status: int, title: str, detail: str | None = None):
        self.status = status
        self.title = title
        self.detail = detail
        super().__init__(f"{status} {title}: {detail}")
```

## Anti-Copy Directive

**DO NOT copy this skeleton verbatim.** It demonstrates auth + error handling only. Generate your endpoint methods from the live `/openapi.json` spec using `openapi-python-client`, then call them through the `_request` method above. Do not hand-write endpoint URLs or request shapes from memory.
