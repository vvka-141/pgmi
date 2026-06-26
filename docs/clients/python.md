---
title: "Python"
description: "Generate Python clients from an advanced-template OpenAPI contract."
weight: 40
---

# Python Client Generation

Generate a typed Python client from your deployment's `/openapi.json`.

## openapi-python-client

Generates a complete Python package with Pydantic models and an httpx-based client.

```bash
pip install openapi-python-client

# Generate client package
openapi-python-client generate \
  --url http://localhost:3000/openapi.json \
  --output-path ./api-client
```

Usage:

```python
from api_client import Client
from api_client.api.default import hello_world

client = Client(base_url="http://localhost:3000")
response = hello_world.sync(client=client, name="World")
print(response.message)

# Async
response = await hello_world.asyncio(client=client, name="World")
```

## Updating

When handlers change, regenerate:

```bash
openapi-python-client update \
  --url http://localhost:3000/openapi.json \
  --output-path ./api-client
```

## Alternative: openapi-generator

```bash
openapi-generator generate \
  -i http://localhost:3000/openapi.json \
  -g python \
  -o ./api-client
```
