---
name: pgmi-handler-patterns
description: "Defensive coding patterns for REST/RPC API handler bodies in the advanced template: input materialization, validation, state probing, safe casting, HTTP status codes, the handler→kernel contract. Load when writing or reviewing api/*-handlers.sql."
user_invocable: true
---

**Purpose**: The authoritative reference for writing handler bodies in pgmi's advanced template. Defines the four-phase pattern (materialize → validate → probe → execute), safe-casting rules, HTTP status-code semantics for CRUD, and the handler → kernel contract.

**Auto-Load With**:
- File patterns: `**/api/*-handlers.sql`, `**/api/*handlers.sql`
- Keywords: "handler", "rest_invoke", "problem_response", "try_cast", "four-phase"

**When to Use**: Writing new handlers, reviewing handler code, debugging 4xx/5xx status-code issues.

> Examples below use generic `core.order` / `core.customer` / `core.project` entities — the same teaching vocabulary as `ARCHITECTURE.md` and `lib/README.md`. The patterns are domain-neutral; substitute your own tables.

---

## Why handler bodies are not ordinary PL/pgSQL

A handler body runs once per HTTP request and must produce an HTTP outcome. It is **not** a place for the imperative, exception-driven style that works in deploy scripts or application code. The single most common failure when an AI assistant (or a developer fluent in Python/JS/Java) writes pgmi handlers is **signalling outcomes by throwing**: a missing record becomes `RAISE EXCEPTION`, a bad UUID becomes an unhandled cast error, and a `BEGIN...EXCEPTION...END` block wraps the whole thing to "catch and translate." In a transactional database that is wrong on four counts (see [EXCEPTION Blocks Are Banned in Handlers](#exception-blocks-are-banned-in-handlers)).

The four-phase pattern replaces all of it. A handler **returns** an `api.http_response` for every outcome — success or failure — and never throws to communicate with the gateway.

---

## The Four-Phase Pattern

Every handler body follows this structure.

```
DECLARE → Materialize → Validate → Probe → Execute
```

| Phase | What happens | Fails with |
|-------|-------------|------------|
| **Materialize** | Extract all inputs into typed local variables using safe casts | — |
| **Validate** | Check required fields are present; check optional field formats | 400, 422 |
| **Probe** | Verify referenced entities exist, target record exists, no conflicts | 404, 409, 422 |
| **Execute** | Call the kernel function, format and return the response | 201/200/204 |

### Why this order matters

Materialization is cheap (local variable assignment). Validation rejects garbage input before any database query runs. Probing confirms state before the mutation. By the time Execute runs, the kernel receives only valid, verified inputs — it never has to handle missing references or bad formats, so it stays pure SQL.

---

## Phase 1: Materialize

Extract **every** input value into a typed local variable at the top of the handler. This creates a single point of truth for "what did the caller send."

### Path parameters

```sql
v_id := common.try_cast(
    (regexp_matches((request).url, '^/orders/([0-9a-f-]+)'))[1],
    NULL::uuid
);
```

### Query parameters

```sql
v_q := api.query_params((request).url);
v_customer_id := common.try_cast(v_q -> 'customerId', NULL::uuid);
```

### Body fields

```sql
v_b := api.content_json((request).content);

v_customer_id := common.try_cast(v_b->>'customerId', NULL::uuid);     -- UUID
v_total       := common.try_cast(v_b->>'total', NULL::numeric);       -- numeric
v_is_priority := common.try_cast(v_b->>'isPriority', NULL::boolean);   -- boolean
v_quantity    := common.try_cast(v_b->>'quantity', NULL::integer);     -- integer
v_due_date    := common.try_cast(v_b->>'dueDate', NULL::timestamp)::date; -- date (see note)
v_note        := v_b->>'note';                                        -- text, no cast needed
```

### `common.try_cast()` overloads

The advanced template provides these overloads (defined in `lib/common/cast.sql`). Each returns the default (pass `NULL::type`) instead of raising on bad input:

| Target type | Call | Returns on bad input |
|-------------|------|---------------------|
| `uuid` | `common.try_cast(text, NULL::uuid)` | NULL |
| `boolean` | `common.try_cast(text, NULL::boolean)` | NULL |
| `integer` | `common.try_cast(text, NULL::integer)` | NULL |
| `bigint` | `common.try_cast(text, NULL::bigint)` | NULL |
| `numeric` | `common.try_cast(text, NULL::numeric)` | NULL |
| `interval` | `common.try_cast(text, NULL::interval)` | NULL |
| `timestamp` | `common.try_cast(text, NULL::timestamp)` | NULL |
| `timestamptz` | `common.try_cast(text, NULL::timestamptz)` | NULL |
| **`date`** | **No overload** — use `common.try_cast(text, NULL::timestamp)::date` | NULL |

> `lib/common/cast.sql` also defines a `?>` try-cast operator in the `api` schema (`text ?> default`). Both the operator and the `common.try_cast()` function resolve in handlers (handler search_path includes `api`). Use whichever form reads better in context.

### Rules

- **Never** use raw `::uuid`, `::boolean`, `::integer`, `::numeric`, `::bigint`, `::timestamp` casts on user-supplied text. They raise on bad input — turning a malformed query param into an unhandled exception (wrong status, leaked internals) instead of a clean 422.
- **Always** materialize into local variables — never reach into `v_b->>'field'` inline in the Execute phase.
- Text fields extracted via `->>` are already text and need no `try_cast`.

---

## Phase 2: Validate

### Required fields

```sql
IF v_customer_id IS NULL THEN
    RETURN api.problem_response(422, 'Unprocessable', 'customerId is required');
END IF;

IF v_name IS NULL OR length(trim(v_name)) = 0 THEN
    RETURN api.problem_response(422, 'Unprocessable', 'name is required');
END IF;
```

### Optional field format validation

When a caller provides a field but `try_cast` returns NULL, the format is bad — distinguish "absent" (fine) from "present but malformed" (422):

```sql
IF v_b->>'projectId' IS NOT NULL AND v_b->>'projectId' <> '' AND v_project_id IS NULL THEN
    RETURN api.problem_response(422, 'Unprocessable', 'projectId is not a valid UUID');
END IF;
```

### Enum / allowlist validation

Validate the raw text against the allowed set **before** casting to the enum type — a raw `::core.order_status` cast would raise on an unknown value:

```sql
IF v_status_raw NOT IN ('draft', 'placed', 'shipped') THEN
    RETURN api.problem_response(422, 'Unprocessable',
        'status must be one of: draft, placed, shipped');
END IF;
v_status := v_status_raw::core.order_status;   -- safe to cast after validation
```

### Business-rule validation

```sql
IF v_total IS NULL OR v_total <= 0 THEN
    RETURN api.problem_response(422, 'Unprocessable', 'total must be a positive number');
END IF;
```

### Field-level validation errors (RFC 9457)

When several fields fail at once, return them together via `api.invalid_param()` so the client can place each error on the right form field:

```sql
RETURN api.problem_response(422, 'Unprocessable', 'Validation failed',
    invalid_params => jsonb_build_array(
        api.invalid_param('customerId', 'is required'),
        api.invalid_param('total', 'must be a positive number')
    ));
```

---

## Phase 3: Probe

Verify the database state allows the operation **before** calling the kernel. Each probe maps to a specific status code.

### Target record existence (update/delete) → 404

```sql
-- The resource the caller is acting ON does not exist
IF NOT EXISTS(SELECT 1 FROM core.order WHERE id = v_id AND deleted_at IS NULL) THEN
    RETURN api.problem_response(404, 'Not Found', 'Order not found');
END IF;
```

### Parent resource existence (sub-resources) → 404

```sql
-- GET /customers/:id/orders — the customer must exist
IF NOT EXISTS(SELECT 1 FROM core.customer WHERE id = v_id AND deleted_at IS NULL) THEN
    RETURN api.problem_response(404, 'Not Found', 'Customer not found');
END IF;
```

### FK reference existence (create/update) → 422

```sql
-- The caller's input points TO an entity that does not exist
IF NOT EXISTS(SELECT 1 FROM core.customer WHERE id = v_customer_id AND deleted_at IS NULL) THEN
    RETURN api.problem_response(422, 'Unprocessable', 'Customer not found');
END IF;

-- Optional FK: only probe when the caller provided it
IF v_project_id IS NOT NULL
   AND NOT EXISTS(SELECT 1 FROM core.project WHERE id = v_project_id AND deleted_at IS NULL) THEN
    RETURN api.problem_response(422, 'Unprocessable', 'Project not found');
END IF;
```

### Uniqueness (create) → 409

Scope the probe to the caller's tenant(s). The advanced template is multi-org, so the tenant anchor is the `api.current_member_org_ids()` array (there is no single "current org"). If your app pins one active org per session, substitute that single id.

```sql
IF EXISTS(SELECT 1 FROM core.order
          WHERE organization_id = ANY (api.current_member_org_ids())
            AND lower(reference) = lower(v_reference)) THEN
    RETURN api.problem_response(409, 'Conflict', 'An order with this reference already exists');
END IF;
```

### Uniqueness (update) — exclude self → 409

```sql
IF v_reference IS NOT NULL AND EXISTS(
    SELECT 1 FROM core.order
    WHERE organization_id = ANY (api.current_member_org_ids())
      AND lower(reference) = lower(v_reference)
      AND deleted_at IS NULL
      AND id <> v_id
) THEN
    RETURN api.problem_response(409, 'Conflict', 'An order with this reference already exists');
END IF;
```

### Duplicate association (link endpoints) → 409

```sql
-- POST /orders/:id/watchers — user already watching
IF EXISTS(SELECT 1 FROM core.order_watcher
          WHERE order_id = v_id AND user_id = v_user_id) THEN
    RETURN api.problem_response(409, 'Conflict', 'User is already a watcher');
END IF;
```

### State-transition guard → 409

```sql
-- Cannot ship an order that is not yet placed
IF v_current.status <> 'placed' THEN
    RETURN api.problem_response(409, 'Conflict',
        format('Cannot ship: current status is %s', v_current.status));
END IF;
```

### Polymorphic entity existence

When a handler serves multiple entity types (e.g. `/{entity}/:id/documents`), centralize the existence check in a helper:

```sql
CREATE OR REPLACE FUNCTION core.documentable_entity_exists(
    p_type core.documentable_entity, p_id uuid
) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT CASE p_type
        WHEN 'order'    THEN EXISTS(SELECT 1 FROM core.order    WHERE id = p_id AND deleted_at IS NULL)
        WHEN 'customer' THEN EXISTS(SELECT 1 FROM core.customer WHERE id = p_id AND deleted_at IS NULL)
        ELSE false
    END;
$$;
```

---

## Phase 4: Execute

Call the kernel; format the response.

```sql
-- Create → 201
v_row := core.create_order(v_customer_id, v_total, v_project_id, v_note);
RETURN api.json_response(201, core.order_json(v_row));

-- Update → 200
v_row := core.update_order(v_id, v_total, v_project_id, v_note);
RETURN api.json_response(200, core.order_json(v_row));

-- Delete → 204
PERFORM core.delete_order(v_id);
RETURN api.json_response(204, '{}'::jsonb);

-- List → 200
RETURN api.json_response(200, jsonb_build_object('orders', v_rows));
```

---

## HTTP Status Code Decision Tree

| Code | When to use | Example |
|------|------------|---------|
| **200** | Successful GET, PUT, action | `GET /orders`, `PUT /orders/:id` |
| **201** | Successful POST that creates a resource | `POST /orders` |
| **204** | Successful DELETE | `DELETE /orders/:id` |
| **400** | Malformed path parameter (e.g. not a UUID) | `v_id IS NULL` after `try_cast` on the path |
| **404** | Target resource (the URL path) does not exist | PUT/DELETE/GET on a non-existent `:id` |
| **404** | Parent resource does not exist | `GET /customers/:id/orders` where the customer is gone |
| **409** | Would violate a uniqueness constraint | POST with a duplicate reference |
| **409** | State does not allow the operation | Ship an already-shipped order |
| **409** | Association already exists | `POST /orders/:id/watchers` when already watching |
| **422** | Required field missing or invalid | `customerId is required` |
| **422** | Optional field provided but bad format | `projectId is not a valid UUID` |
| **422** | Referenced entity (FK) does not exist | `Customer not found` |

### 404 vs 422 for "not found"

- **404** — the resource identified by the URL path does not exist (the thing the caller is acting *on*).
- **422** — a referenced entity in the request body does not exist (a thing the caller is pointing *to*).

---

## EXCEPTION Blocks Are Banned in Handlers

**Do not use `BEGIN...EXCEPTION...END` blocks in handler bodies.** They are:

1. **Expensive** — PostgreSQL opens an implicit savepoint for every EXCEPTION block. On a path that runs once per request, that overhead is gratuitous.
2. **Wrong status codes** — an exception from a missing FK surfaces as a generic `23503`, which a gateway maps to 400. The correct answer is a 422 that names the missing reference.
3. **Information-lossy** — `SQLERRM` carries database internals (`violates foreign key constraint "fk_order_customer"`) that leak schema details to the client and help no one.
4. **A crutch that hides bugs** — if the kernel throws something unexpected, you want it to surface as a sanitized 500, not be swallowed into a misleading 400/404.

The four-phase pattern replaces exception handling entirely: by the time Execute runs, every input is valid and every precondition holds. If the kernel still throws, it is a genuine server error and should propagate to the gateway, which sanitizes it into a 500.

> **Where EXCEPTION blocks *do* belong:** deploy-time orchestration, migration scripts, and background/queue processing — see `pgmi-sql`. The SQLSTATE→HTTP mapping in `pgmi-sql` is a **gateway-level** concern (the catch-all that wraps handler invocation), **not** something you write inside a handler body.

---

## Handler → Kernel Contract

| Layer | Responsibility | Returns |
|-------|---------------|---------|
| **Handler** (`api/`) | Parse input, validate, probe state, format response | `api.http_response` |
| **Kernel** (`core/`) | Perform the business operation; assume inputs are valid | Entity rowtype (`core.order`, …) |

Handlers call kernel functions — they **do not** `INSERT`/`UPDATE`/`DELETE` physical tables directly. The kernel is the unit of business logic; the handler is the unit of HTTP protocol compliance. Kernels return the **full entity row** (`RETURNS core.order`), never just a `uuid`, `void`, or status flag — the handler needs the whole tuple to render the JSON response.

```sql
-- Handler calls kernel
v_row := core.create_order(v_customer_id, v_total, v_project_id, v_note);

-- Kernel returns the full entity (in core/<domain>/02-*-kernel.sql), pure SQL
CREATE FUNCTION core.create_order(p_customer_id uuid, p_total numeric, p_project_id uuid, p_note text)
RETURNS core.order LANGUAGE sql AS $$
    INSERT INTO core.order (customer_id, total, project_id, note)
    VALUES (p_customer_id, p_total, p_project_id, p_note)
    RETURNING *;
$$;
```

---

## Complete Example: Create Handler

```sql
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'c1000090-0002-4000-8000-000000000001',
        'uri', '^/orders(\?.*)?$',
        'httpMethod', '^POST$',
        'name', 'create_order',
        'description', 'Create a new order',
        'requiresAuth', true
    ),
    $body$
DECLARE
    v_b           jsonb := api.content_json((request).content);
    v_customer_id uuid;
    v_total       numeric;
    v_project_id  uuid;
    v_note        text;
    v_row         core.order;
BEGIN
    -- Materialize
    v_customer_id := common.try_cast(v_b->>'customerId', NULL::uuid);
    v_total       := common.try_cast(v_b->>'total', NULL::numeric);
    v_project_id  := common.try_cast(v_b->>'projectId', NULL::uuid);
    v_note        := v_b->>'note';

    -- Validate required
    IF v_customer_id IS NULL THEN
        RETURN api.problem_response(422, 'Unprocessable', 'customerId is required');
    END IF;
    IF v_total IS NULL OR v_total <= 0 THEN
        RETURN api.problem_response(422, 'Unprocessable', 'total must be a positive number');
    END IF;

    -- Validate optional field formats
    IF v_b->>'projectId' IS NOT NULL AND v_b->>'projectId' <> '' AND v_project_id IS NULL THEN
        RETURN api.problem_response(422, 'Unprocessable', 'projectId is not a valid UUID');
    END IF;

    -- Probe state
    IF NOT EXISTS(SELECT 1 FROM core.customer WHERE id = v_customer_id AND deleted_at IS NULL) THEN
        RETURN api.problem_response(422, 'Unprocessable', 'Customer not found');
    END IF;
    IF v_project_id IS NOT NULL
       AND NOT EXISTS(SELECT 1 FROM core.project WHERE id = v_project_id AND deleted_at IS NULL) THEN
        RETURN api.problem_response(422, 'Unprocessable', 'Project not found');
    END IF;

    -- Execute
    v_row := core.create_order(v_customer_id, v_total, v_project_id, v_note);
    RETURN api.json_response(201, core.order_json(v_row));
END;
    $body$
);
```

---

## Complete Example: Update Handler

```sql
    $body$
DECLARE
    v_id         uuid;
    v_b          jsonb := api.content_json((request).content);
    v_total      numeric;
    v_project_id uuid;
    v_note       text;
    v_row        core.order;
BEGIN
    -- Materialize path param
    v_id := common.try_cast(
        (regexp_matches((request).url, '^/orders/([0-9a-f-]+)'))[1], NULL::uuid);
    IF v_id IS NULL THEN
        RETURN api.problem_response(400, 'Bad Request', 'Invalid order id');
    END IF;

    -- Probe: target must exist
    IF NOT EXISTS(SELECT 1 FROM core.order WHERE id = v_id AND deleted_at IS NULL) THEN
        RETURN api.problem_response(404, 'Not Found', 'Order not found');
    END IF;

    -- Materialize body (NULL = "don't change")
    v_total      := common.try_cast(v_b->>'total', NULL::numeric);
    v_project_id := common.try_cast(v_b->>'projectId', NULL::uuid);
    v_note       := v_b->>'note';

    -- Validate optional field formats
    IF v_b->>'total' IS NOT NULL AND v_b->>'total' <> '' AND v_total IS NULL THEN
        RETURN api.problem_response(422, 'Unprocessable', 'total is not a valid number');
    END IF;
    IF v_b->>'projectId' IS NOT NULL AND v_b->>'projectId' <> '' AND v_project_id IS NULL THEN
        RETURN api.problem_response(422, 'Unprocessable', 'projectId is not a valid UUID');
    END IF;

    -- Probe FK references (only when the caller wants to change them)
    IF v_project_id IS NOT NULL
       AND NOT EXISTS(SELECT 1 FROM core.project WHERE id = v_project_id AND deleted_at IS NULL) THEN
        RETURN api.problem_response(422, 'Unprocessable', 'Project not found');
    END IF;

    -- Execute
    v_row := core.update_order(v_id, v_total, v_project_id, v_note);
    RETURN api.json_response(200, core.order_json(v_row));
END;
    $body$
```

---

## Complete Example: Delete Handler

```sql
    $body$
DECLARE
    v_id uuid;
BEGIN
    v_id := common.try_cast(
        (regexp_matches((request).url, '^/orders/([0-9a-f-]+)'))[1], NULL::uuid);
    IF v_id IS NULL THEN
        RETURN api.problem_response(400, 'Bad Request', 'Invalid order id');
    END IF;

    IF NOT EXISTS(SELECT 1 FROM core.order WHERE id = v_id AND deleted_at IS NULL) THEN
        RETURN api.problem_response(404, 'Not Found', 'Order not found');
    END IF;

    PERFORM core.delete_order(v_id);
    RETURN api.json_response(204, '{}'::jsonb);
END;
    $body$
```

---

## Complete Example: List with Pagination + Filter Validation

Use `api.pagination_params()` (defined in `lib/api/07-helpers.sql`) so every list endpoint parses `?limit`/`?offset` the same way: clamped to sane bounds, a 422 on non-integer input, never a raw cast.

```sql
    $body$
DECLARE
    v_q           extensions.hstore;
    v_customer_id uuid;
    v_page        record;
    v_limit       integer;
    v_offset      integer;
    v_rows        jsonb;
BEGIN
    v_q := api.query_params((request).url);

    -- Pagination. o_error is a composite (api.http_response), so assign the
    -- whole row to a record — a composite cannot be a target in a multi-column
    -- SELECT ... INTO a, b, c list (each of those must be scalar).
    v_page := api.pagination_params(v_q);
    IF (v_page.o_error).status_code IS NOT NULL THEN
        RETURN v_page.o_error;
    END IF;
    v_limit  := v_page.o_limit;
    v_offset := v_page.o_offset;

    -- Optional filter: validate format, but a missing FK just returns empty
    v_customer_id := common.try_cast(v_q -> 'customerId', NULL::uuid);
    IF v_q -> 'customerId' IS NOT NULL AND v_q -> 'customerId' <> '' AND v_customer_id IS NULL THEN
        RETURN api.problem_response(422, 'Unprocessable', 'customerId is not a valid UUID');
    END IF;

    -- Execute (fetch limit+1 to compute hasMore)
    SELECT COALESCE(jsonb_agg(core.order_json(o.*) ORDER BY o.created_at DESC), '[]'::jsonb)
    INTO v_rows
    FROM (
        SELECT * FROM core.vw_order_list
        WHERE (v_customer_id IS NULL OR customer_id = v_customer_id)
        ORDER BY created_at DESC
        LIMIT v_limit + 1 OFFSET v_offset
    ) o;

    RETURN api.json_response(200, jsonb_build_object(
        'orders',  (SELECT jsonb_agg(e) FROM (SELECT * FROM jsonb_array_elements(v_rows) LIMIT v_limit) e),
        'hasMore', jsonb_array_length(v_rows) > v_limit
    ));
END;
    $body$
```

---

## Partial Update (PATCH semantics)

Update handlers use the convention `NULL = "don't change"`. Absent body fields materialize to NULL; the kernel uses `COALESCE(p_param, existing_column)` to preserve the current value:

```sql
-- Kernel: core.update_order
UPDATE core.order SET
    total      = COALESCE(p_total, total),
    project_id = COALESCE(p_project_id, project_id),
    note       = COALESCE(p_note, note),
    updated_at = now()
WHERE id = p_id
RETURNING *;
```

The handler validates optional field **formats** but never requires them — only fields the caller sends are changed.

### Clearing a nullable field

COALESCE means a caller cannot clear a nullable field by omitting it. To allow explicit clearing, use a dedicated sentinel or a separate endpoint — never overload NULL to mean both "absent" and "set to null."

---

## Link / Unlink Sub-Resource Handlers

Association endpoints (POST/DELETE for many-to-many relationships) follow a simplified four-phase pattern:

```sql
-- POST /orders/:orderId/watchers — add a watcher
DECLARE
    v_order_id uuid;
    v_user_id  uuid;
    v_b        jsonb := api.content_json((request).content);
BEGIN
    -- Materialize
    v_order_id := common.try_cast(
        (regexp_matches((request).url, '^/orders/([0-9a-f-]+)/watchers'))[1], NULL::uuid);
    v_user_id  := common.try_cast(v_b->>'userId', NULL::uuid);

    -- Validate
    IF v_order_id IS NULL THEN RETURN api.problem_response(400, 'Bad Request', 'Invalid order id'); END IF;
    IF v_user_id  IS NULL THEN RETURN api.problem_response(422, 'Unprocessable', 'userId is required'); END IF;

    -- Probe
    IF NOT EXISTS(SELECT 1 FROM core.order WHERE id = v_order_id AND deleted_at IS NULL) THEN
        RETURN api.problem_response(404, 'Not Found', 'Order not found');
    END IF;
    IF EXISTS(SELECT 1 FROM core.order_watcher WHERE order_id = v_order_id AND user_id = v_user_id) THEN
        RETURN api.problem_response(409, 'Conflict', 'User is already a watcher');
    END IF;

    -- Execute
    PERFORM core.add_order_watcher(v_order_id, v_user_id);
    RETURN api.json_response(201, jsonb_build_object('orderId', v_order_id, 'userId', v_user_id));
END;
```

The **unlink** handler (DELETE) probes for existence instead of conflict — 404 if the association does not exist, 204 on success.

---

## Concurrency: probe-then-mutate is not atomic

A Probe followed by an Execute is a read-check-write across two statements. Under concurrent requests the check can pass for two callers at once (TOCTOU), producing lost updates or duplicate rows — a class of bug that single-session tests never reveal.

For **state-machine mutations** (approve, ship, mark-paid, convert, import), the kernel must lock and re-check atomically rather than rely on the handler probe alone:

```sql
-- Kernel: lock the row, then guard the transition in the UPDATE itself
UPDATE core.order SET status = 'shipped', shipped_at = now()
WHERE id = p_id AND status = 'placed'     -- guard predicate makes the write idempotent
RETURNING *;
-- 0 rows back → the precondition no longer holds → handler returns 409
```

The handler probe still gives a clean 409 in the common case; the guard predicate closes the race in the rare one. See `pgmi-postgres-review` for the concurrency checklist.

---

## Handler Test Patterns

Handler tests exercise the HTTP compliance surface — status codes, required-field validation, probe behavior, and **no internal leakage**:

```sql
DO $$
DECLARE
    v_h    extensions.hstore := ('x-user-id=>dev|superuser')::extensions.hstore;
    v_resp api.http_response;
    v_nil  uuid := '00000000-0000-0000-0000-000000000000';
BEGIN
    PERFORM set_config('auth.idp_subject', 'dev|superuser', true);

    -- Missing required field → 422
    v_resp := api.rest_invoke('POST', '/orders', v_h, '{}'::jsonb);
    IF (v_resp).status_code <> 422 THEN
        RAISE EXCEPTION 'POST /orders empty body expected 422, got %', (v_resp).status_code;
    END IF;

    -- Non-existent target → 404
    v_resp := api.rest_invoke('DELETE', '/orders/' || v_nil::text, v_h, NULL::bytea);
    IF (v_resp).status_code <> 404 THEN
        RAISE EXCEPTION 'DELETE non-existent expected 404, got %', (v_resp).status_code;
    END IF;

    -- Bad UUID in body must produce a clean 422, not a leaked cast error
    v_resp := api.rest_invoke('POST', '/orders', v_h, '{"customerId":"not-a-uuid"}'::jsonb);
    IF (v_resp).status_code NOT IN (422) THEN
        RAISE EXCEPTION 'bad UUID expected 422, got %', (v_resp).status_code;
    END IF;
    IF api.content_json((v_resp).content)->>'detail' ~* 'PL/pgSQL|CONTEXT|ERROR|SQLSTATE' THEN
        RAISE EXCEPTION 'error response leaks internals';
    END IF;
END $$;
```

---

## Integration with Other Skills

- **`pgmi-api-architecture`** — protocol design, layered architecture, rich documents.
- **`pgmi-sql`** — SQL/PL/pgSQL conventions. Its EXCEPTION and SQLSTATE→HTTP patterns are for **deploy-time and gateway-level** code, not handler bodies.
- **`pgmi-postgres-review`** — review checklist, including the handler-compliance and concurrency sections.
- **`pgmi-testing-review`** — writing and debugging handler tests.
