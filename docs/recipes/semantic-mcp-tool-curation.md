---
title: "Semantic MCP curation"
description: "Recipe for surfacing relevant MCP tools with embedding-based curation."
weight: 10
---

# Recipe: Semantic Agent-Tool Curation

> **Use this only at tool-overload scale.** When an agent has *dozens* of tools,
> exposing all of them at once dilutes the model's attention and inflates token
> cost. This recipe surfaces the relevant subset for a given query. If your
> project has a handful of tools, skip it — expose handlers directly through the
> existing MCP/RPC routing and let the client list them all.

This recipe is a **cookbook pattern**, not part of the advanced template. It
adds an embedding dependency (pgvector + an external embedding model) that
narrows managed-cloud compatibility and adds runtime cost, so it is deliberately
kept out of `internal/scaffold/templates/`. Build it in your own project on top
of the handler registry the advanced template already ships.

The design has three layers, each independently useful:

1. **The marker** — a protocol-agnostic way to say "this handler is an agent
   tool." No embedding dependency.
2. **The catalog + curator** — a parameterized view that ranks marked tools by
   similarity to a query. Ordering, threshold, and `LIMIT` stay caller-side.
3. **The provider** — a swappable embedding store/model. pgvector is *your*
   opt-in dependency, not the template's.

---

## Layer 1 — Mark a handler as an agent tool

The reusable idea is to decouple "agent-callable" from the routing protocol.
Add a marker table and a thin registrar that wraps the existing handler
registrar, validating two agent-only fields:

- `category` — groups tools by domain (`time`, `math`, `memory`, …) for cheap
  non-semantic filtering.
- `toolDescription` — LLM-targeted prose used for semantic ranking (distinct
  from the human `description`).

```sql
CREATE TABLE IF NOT EXISTS api.agent_tool (
    handler_object_id uuid PRIMARY KEY
        REFERENCES api.handler(object_id) ON DELETE CASCADE,
    category          text NOT NULL,
    is_active         boolean NOT NULL DEFAULT true,
    input_schema      jsonb,
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- Thin wrapper over the protocol registrar (RPC shown; mcp works identically).
CREATE OR REPLACE FUNCTION api.create_or_replace_agent_tool(
    p_metadata jsonb, p_handler_body text
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
DECLARE
    v_id       uuid := (p_metadata->>'id')::uuid;
    v_category text := p_metadata->>'category';
    v_tooldesc text := p_metadata->>'toolDescription';
BEGIN
    IF v_category IS NULL OR v_tooldesc IS NULL THEN
        RAISE EXCEPTION 'agent tool requires "category" and "toolDescription"';
    END IF;

    -- Strip the agent-only keys; the schemas flow through to the real handler.
    PERFORM api.create_or_replace_rpc_handler(
        p_metadata - 'category' - 'toolDescription', p_handler_body);

    INSERT INTO api.agent_tool (handler_object_id, category, input_schema)
    VALUES (v_id, v_category, p_metadata->'inputSchema')
    ON CONFLICT (handler_object_id) DO UPDATE
        SET category = EXCLUDED.category,
            input_schema = EXCLUDED.input_schema,
            is_active = true;

    -- Store toolDescription wherever your embedding pipeline reads from
    -- (a passages table keyed by a content fingerprint works well).
END;
$$;
```

This layer has **no embedding dependency** — you can ship the marker and select
tools by `category` alone before adding semantics.

> **`x-include-schema` note.** If you also inject
> `responseHeaders.x-include-schema=true` so JSON-RPC results embed `$schema`
> for the model, use the **lowercase** key until the case-normalization fix
> lands (see PGMI-10 Part A). The advanced MCP path exposes `outputSchema` at
> discovery via `outputSchema`/`structuredContent`, which is the correct channel
> for MCP tools; response-body `$schema` injection is an RPC/REST concern.

---

## Layer 2 — Curate with a pluggable strategy

Join the marker to the handler and to your embedding store, then expose a
**parameterized view** that projects a similarity column. Per the `pvw_`
convention, the function carries *no* ordering, threshold, or `LIMIT` — those
are the caller's choice, so the same catalog supports semantic, keyword, or
category-filter selection.

```sql
CREATE OR REPLACE VIEW api.vw_agent_tool AS
SELECT at.handler_object_id,
       COALESCE(rr.method_name, mr.mcp_name) AS name,   -- protocol-agnostic
       h.description,
       COALESCE(at.input_schema, h.input_json_schema::jsonb) AS input_schema,
       at.category,
       e.embedding                                       -- from your provider
FROM api.agent_tool at
JOIN api.handler h ON h.object_id = at.handler_object_id
LEFT JOIN api.rpc_route rr ON rr.handler_object_id = at.handler_object_id
LEFT JOIN api.mcp_route mr ON mr.handler_object_id = at.handler_object_id
LEFT JOIN embeddings.tool_embedding e
       ON e.handler_object_id = at.handler_object_id
WHERE at.is_active AND h.deleted_at IS NULL;

-- Pure projection: similarity only. Caller adds ORDER BY / WHERE / LIMIT.
CREATE OR REPLACE FUNCTION api.pvw_list_agent_tools(
    p_query_embedding embeddings.text_embedding DEFAULT NULL
) RETURNS TABLE (handler_object_id uuid, name text, description text,
                 input_schema jsonb, category text, similarity numeric)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = api, embeddings, extensions, pg_temp
AS $$
    SELECT v.handler_object_id, v.name, v.description, v.input_schema, v.category,
           CASE WHEN p_query_embedding IS NULL OR v.embedding IS NULL THEN NULL
                ELSE ROUND((1 - (v.embedding <=> p_query_embedding))::numeric, 4)
           END AS similarity
    FROM api.vw_agent_tool v;
$$;
```

A handler (REST/RPC) or `tools/call` then curates with caller-side predicates:

```sql
SELECT * FROM api.pvw_list_agent_tools($1)
 WHERE similarity IS NULL OR similarity >= 0.2     -- threshold (caller's)
 ORDER BY similarity DESC NULLS LAST, name         -- ranking  (caller's)
 LIMIT 12;                                         -- budget   (caller's)
```

A `NULL` query embedding yields `NULL` similarity for every row, so the same
function degrades gracefully to "list everything" before embeddings are warm.

---

## Layer 3 — Abstract the embedding provider

Model the embedding as a **contract**, not a vendor. Two pieces:

- A `text_embedding` type (a pgvector `vector(N)` of whatever dimension your
  model emits) and a store keyed by a stable content fingerprint.
- A backend warmup loop: list passages lacking an embedding, embed them with
  *your* provider, and upsert the result.

```sql
-- pgvector is YOUR opt-in dependency (managed-cloud-conditional — not every
-- provider offers it). Pick the dimension to match your chosen model.
CREATE EXTENSION IF NOT EXISTS vector;
CREATE DOMAIN embeddings.text_embedding AS extensions.vector(/* model dim */);

CREATE TABLE IF NOT EXISTS embeddings.tool_embedding (
    handler_object_id uuid PRIMARY KEY,
    embedding         embeddings.text_embedding NOT NULL,
    embedded_at       timestamptz NOT NULL DEFAULT now()
);

-- Generic warmup surface — the backend stays the only place that knows the
-- model name/vendor. SQL never names a provider.
CREATE OR REPLACE FUNCTION api.list_pending_tool_embeddings()
RETURNS TABLE (handler_object_id uuid, tool_description text) ...;

CREATE OR REPLACE FUNCTION api.upsert_tool_embedding(
    p_handler_object_id uuid, p_embedding embeddings.text_embedding
) RETURNS void ...;
```

Keep the model choice (provider, dimension) entirely in the warmup worker.
Swapping providers is then a worker change plus a one-time re-embed — no SQL
churn. Document pgvector as a managed-cloud-conditional dependency; see
[Production](../PRODUCTION.md) for which providers offer it.

---

## When *not* to use this

- **Few tools.** Below ~a couple dozen tools, the model handles the full list
  fine. Expose handlers directly via MCP routing and skip all three layers.
- **No managed-cloud pgvector.** If your target can't install pgvector, keep
  Layer 1 (the marker) and curate by `category` only — still useful, zero new
  dependency.

---

## Source

Adapted (provider-abstracted) from the `home-server` project's
`api/agent-tool-marker.sql` and `api/agent-tool-curator.sql`, a production user
of the advanced template. The marker lives outside `lib/` there for the same
reason it lives in `docs/` here: the base template stays dependency-free.
