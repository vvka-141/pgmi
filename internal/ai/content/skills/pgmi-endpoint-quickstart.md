---
name: pgmi-endpoint-quickstart
description: "End-to-end recipe for adding a domain entity plus a REST endpoint and test to the advanced template — the three layers (core kernel, api handler, __test__), sort keys, the four-phase handler body, auth, and the test idiom in one place. Load before building your first advanced-template endpoint."
user_invocable: true
---

# Endpoint Quickstart (advanced template)

The fastest correct path from "add a feature" to a deployed, tested REST endpoint
in the advanced template. This consolidates what is otherwise spread across
`pgmi-handler-patterns`, `pgmi-api-architecture`, `pgmi-metadata-system`, and the
template source. For the full doctrine behind each phase, load
`pgmi-handler-patterns`.

## The three layers

A feature touches three files:

| Layer | File | sortKey | Does |
|-------|------|---------|------|
| **Kernel** | `core/<entity>.sql` | `005/005` | Table + `core.create_<entity>(...)` + `core.<entity>_json(<entity>)` |
| **Handler** | `api/<entity>.sql` | `005/010` | Register REST handlers that call the kernel and format responses |
| **Test** | `api/__test__/test_<entity>.sql` | *(none)* | Drive the endpoint via `api.rest_invoke`, assert status + body |

Handlers **format and validate**; they never touch physical tables directly —
they call `core.*` kernel functions that return the full entity rowtype and do
the DML. This is the handler→kernel contract (`pgmi-handler-patterns`).

**Sort keys — what actually works (verified against a live deploy):**

- The kernel **and** the handler both live in the **user band `005+`** (the
  framework reserves `001–004`; a file keyed `003/0xx` is dropped from
  `pgmi_plan_view` and silently never runs). Key the kernel **before** the
  handler (`005/005` → `005/010`) so the entity type exists when the handler is
  registered — registration compiles the body, so `core.<entity>` must already
  exist.
- The `<pgmi-meta>` block **must be a `/* … */` block comment**. A single-line
  `-- <pgmi-meta>` is ignored by the parser, the file gets no sort key, and it
  falls out of the plan with no error. The deploy just skips it.
- **Test files take no `<pgmi-meta>`** — they are discovered by the `__test__/`
  directory and ordered by path.

Prefer `pgmi metadata scaffold` / `validate` / `plan` over hand-authoring the
block, and run `pgmi metadata plan` to confirm both files appear in execution
order before deploying.

---

## 1. Kernel — `core/product.sql`

```sql
/*
<pgmi-meta id="<uuid>" idempotent="true">
  <sortKeys><key>005/005</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE IF NOT EXISTS core.product (
    object_id  core.entity_id PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    name       text    NOT NULL,
    price      numeric(12,2) NOT NULL
);
-- created_at / deleted_at are auto-injected by the deploy-end sweep for tables
-- keyed on core.entity_id. Query live rows with `deleted_at IS NULL`.

CREATE OR REPLACE FUNCTION core.create_product(p_name text, p_price numeric)
RETURNS core.product LANGUAGE sql AS $$
    INSERT INTO core.product (name, price) VALUES (p_name, p_price)
    RETURNING *;
$$;

CREATE OR REPLACE FUNCTION core.product_json(p core.product)
RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_build_object(
        'id',    p.object_id,
        'name',  p.name,
        'price', p.price
    );
$$;
```

Pass the **row** to the formatter (`core.product_json(p)`), not `p.*` — a single
composite argument is unambiguous; `.*` row-expansion is not.

---

## 2. Handler — `api/product.sql`

`api.create_or_replace_rest_handler(metadata jsonb, body text)`. Metadata keys are
**camelCase**; the body is the *inside* of a function (`DECLARE … BEGIN … END;`),
dollar-quoted with `$body$`, with a single `request` parameter.

```sql
/*
<pgmi-meta id="<uuid>" idempotent="true">
  <sortKeys><key>005/010</key></sortKeys>
</pgmi-meta>
*/

-- GET /products — public list
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', '<uuid>', 'name', 'list_products',
        'uri', '^/products(\?.*)?$', 'httpMethod', '^GET$',
        'requiresAuth', false,                      -- defaults to TRUE; set false for public
        'outputSchema', jsonb_build_object(         -- REQUIRED on every REST handler
            'type', 'object',
            'properties', jsonb_build_object(
                'products', jsonb_build_object('type', 'array',
                    'items', jsonb_build_object('type', 'object'))),
            'required', jsonb_build_array('products'))
    ),
    $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object(
        'products', COALESCE((
            SELECT jsonb_agg(core.product_json(p) ORDER BY p.name)
            FROM core.product p WHERE p.deleted_at IS NULL
        ), '[]'::jsonb)));
END;
    $body$
);

-- POST /products — authenticated create with validation
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', '<uuid>', 'name', 'create_product',
        'uri', '^/products(\?.*)?$', 'httpMethod', '^POST$',
        'requiresAuth', true,
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'name',  jsonb_build_object('type', 'string'),
                'price', jsonb_build_object('type', 'number')),
            'required', jsonb_build_array('name', 'price')),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'id',    jsonb_build_object('type', 'string', 'format', 'uuid'),
                'name',  jsonb_build_object('type', 'string'),
                'price', jsonb_build_object('type', 'number')),
            'required', jsonb_build_array('id', 'name', 'price'))
    ),
    $body$
DECLARE
    v_b     jsonb := api.content_json((request).content);
    v_name  text;
    v_price numeric;
    v_row   core.product;
BEGIN
    -- Phase 1: materialize (never raw-cast user input — try_cast returns NULL)
    v_name  := v_b->>'name';
    v_price := common.try_cast(v_b->>'price', NULL::numeric);

    -- Phase 2: validate (RFC 9457 problem+json; one RETURN per outcome)
    IF v_name IS NULL OR length(trim(v_name)) = 0 THEN
        RETURN api.problem_response(422, 'Unprocessable', 'name is required',
            invalid_params => jsonb_build_array(api.invalid_param('name', 'is required')));
    END IF;
    IF v_b->>'price' IS NULL OR v_b->>'price' = '' THEN
        RETURN api.problem_response(422, 'Unprocessable', 'price is required',
            invalid_params => jsonb_build_array(api.invalid_param('price', 'is required')));
    END IF;
    IF v_price IS NULL THEN
        RETURN api.problem_response(422, 'Unprocessable', 'price must be a number',
            invalid_params => jsonb_build_array(api.invalid_param('price', 'must be a number')));
    END IF;
    IF v_price <= 0 THEN
        RETURN api.problem_response(422, 'Unprocessable', 'price must be positive',
            invalid_params => jsonb_build_array(api.invalid_param('price', 'must be greater than 0')));
    END IF;

    -- Phase 4: execute via the kernel, then format
    v_row := core.create_product(v_name, v_price);
    RETURN api.json_response(201, core.product_json(v_row));
END;
    $body$
);
```

**No `EXCEPTION` blocks in handler bodies** — every path is a `RETURN`, never a
`RAISE`. SQLSTATE→HTTP mapping is the gateway's job. (Phase 3 "probe" — e.g.
`SELECT … WHERE deleted_at IS NULL` to 404 a missing resource — is shown in
`pgmi-handler-patterns`; this create has nothing to probe.)

### Metadata defaults that bite

| Key | Default | Note |
|-----|---------|------|
| `requiresAuth` | **`true`** | Omit it and the endpoint is authenticated (401 without identity). |
| `autoLog` | **`true`** | Request logging. |
| `httpMethod` | `^(GET\|POST\|PUT\|DELETE\|PATCH)$` | POSIX regex; anchor your own (`^POST$`). |
| `name` | — | `api.<name>`; `^[a-zA-Z][a-zA-Z0-9_.-]{0,48}$` (≤49 chars). |
| `outputSchema` | — | **Required on every REST handler** — the OpenAPI test fails the deploy (`REST handlers without output schema: …`) if any handler omits it. |
| `inputSchema` | — | JSON Schema for the request body (declare it on write endpoints). Validated at registration; empty `{}` is rejected. |
| `requiredTransactionIsolation` | — (no floor) | Isolation floor (`read committed` / `repeatable read` / `serializable`). The caller must open the transaction at ≥ this level or the gateway returns 428 (`pgmi.transaction_isolation_too_weak`). |
| `pathParams` | — (positional) | Names for the `uri`'s capture groups, in order: `'pathParams', jsonb_build_array('orgId', 'userId')` turns `^/orgs/([^/]+)/users/(\d+)$` into `/orgs/{orgId}/users/{userId}` in the OpenAPI document, with a required `in: path` parameter for each. Omit it and they are named `{p1}`, `{p2}`, …. Declaring the wrong number of names is rejected at registration. |

---

## 3. Auth model

`requiresAuth: true` ⇒ the gateway returns 401 unless a user resolves. Identity
enters as the `x-user-id` header in **`provider|subject`** form (e.g.
`google|alice-123`); a raw value without `|` fails closed. The gateway sets
`auth.idp_subject` and resolves a membership user. In handlers, read identity via
`api.vw_current_user` and scope to the caller's orgs with
`WHERE org_id = ANY(api.current_member_org_ids())` — the template is
**multi-organization**; there is no single tenant GUC.

---

## 4. Test — `api/__test__/test_product.sql`

Tests run inside savepoints (rolled back automatically). Success is signalled by
the deploy reaching exit 0 — there is no "passed" print at default
`client_min_messages`. A `_setup.sql` fixture in the same directory provides
`test.alice_subject`.

```sql
DO $$
DECLARE
    v_subject text := current_setting('test.alice_subject');
    v_headers extensions.hstore := ('x-user-id=>' || v_subject)::extensions.hstore;
    v_resp    api.http_response;
    v_body    jsonb;
BEGIN
    PERFORM set_config('auth.idp_subject', v_subject, true);

    -- success: POST valid → 201
    v_resp := api.rest_invoke('POST', '/products', v_headers,
        '{"name":"Widget","price":9.99}'::jsonb);
    IF (v_resp).status_code <> 201 THEN
        RAISE EXCEPTION 'expected 201, got %', (v_resp).status_code;
    END IF;
    v_body := api.content_json((v_resp).content);
    IF v_body->>'name' <> 'Widget' THEN
        RAISE EXCEPTION 'name mismatch: %', v_body;
    END IF;

    -- validation error: missing price → 422 with invalid-params, no internals leak
    v_resp := api.rest_invoke('POST', '/products', v_headers, '{"name":"NoPrice"}'::jsonb);
    IF (v_resp).status_code <> 422 THEN
        RAISE EXCEPTION 'expected 422, got %', (v_resp).status_code;
    END IF;
    v_body := api.content_json((v_resp).content);
    IF v_body->'invalid-params'->0->>'name' <> 'price' THEN
        RAISE EXCEPTION 'expected invalid-params for price, got %', v_body;
    END IF;
    IF v_body->>'detail' ~* 'PL/pgSQL|SQLSTATE|CONTEXT' THEN
        RAISE EXCEPTION 'error body leaks internals: %', v_body->>'detail';
    END IF;

    -- unauthenticated POST → 401
    v_resp := api.rest_invoke('POST', '/products', ''::extensions.hstore,
        '{"name":"Widget","price":1.00}'::jsonb);
    IF (v_resp).status_code <> 401 THEN
        RAISE EXCEPTION 'expected 401, got %', (v_resp).status_code;
    END IF;
END $$;
```

`api.rest_invoke(method, url, headers hstore, content)` — the `content` argument
overloads accept `jsonb` or `bytea`. Always assert that error bodies do **not**
leak `PL/pgSQL|SQLSTATE|CONTEXT`.

---

## Deploy & verify

Deploy the project (target a non-management DB):

```bash
pgmi deploy . --connection "postgresql://user@host/postgres" -d mydb --overwrite --force
```

A green run ends with the `DONE` banner and exit 0; a failing test exits 13. The
first deploy prints `DROP FUNCTION … skipping` notices for the handler registry —
those are normal, not errors.

## Related

- `pgmi-handler-patterns` — the full four-phase doctrine, status-code tree, probe/concurrency patterns
- `pgmi-api-architecture` — REST/RPC/MCP protocol design and the gateway/auth model
- `pgmi-metadata-system` — `<pgmi-meta>` blocks and sort-key ordering
- `pgmi-testing-review` — test organization and `_setup.sql` fixtures
