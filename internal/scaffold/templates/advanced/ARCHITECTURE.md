# Architecture: Application as Dataset

This document describes the architectural philosophy behind the advanced template. Understanding these principles is essential for building robust, testable, and maintainable systems with pgmi.

## Core Philosophy

**Your application IS the PostgreSQL database.**

This is one architecture pgmi enables, not one it requires — basic deployments use none of it. Adopt the parts that fit your project and drop the rest.

In this paradigm:
- The database is not "storage for your application" - it IS the application
- All application state lives in PostgreSQL as a carefully modeled dataset
- External actors (users, background jobs, AI agents) modify the dataset through well-defined transactions
- REST, RPC, and MCP endpoints are merely **trigger mechanisms** for these transactions
- Services outside PostgreSQL (web servers, job runners, UIs) are secondary - they invoke transactions but don't own business logic

### Why This Matters

In a traditional stack, business logic spreads across the middle tier (web
servers, services, ORM) and the database is reduced to storage. In the
dataset-centric architecture, protocols trigger PostgreSQL transactions and
the complexity lives where the data lives:

![Traditional stack versus application-as-a-dataset](https://vvka-141.github.io/pgmi/docs/diagrams/d10-application-as-dataset.drawio.svg)

**Benefits:**
- **Transactional guarantees**: Every state change is atomic, consistent, isolated, durable
- **Single source of truth**: No state synchronization between services
- **Testable**: If transactions pass tests, the system is fundamentally healthy
- **Auditable**: Every script execution is logged to `internal.deployment_script_execution_log` with UUID, checksum, timestamp, and executing role
- **Simpler debugging**: State is always in the database, never "in flight"

## Transaction-Centric Design

Every modification to your dataset happens through a **transaction** - a well-defined operation that transitions the dataset from one valid state to another valid state.

### The Golden Rule

> If a transaction succeeds, the dataset is in a valid state.
> If a transaction fails, the dataset remains unchanged.
> There is no third option.

### What This Means in Practice

1. **Design transactions first**: Before building UI or services, define the transactions that modify your dataset
2. **Test transactions thoroughly**: pgmi transactional tests verify every state transition
3. **Expose transactions via protocols**: REST/RPC/MCP handlers call these transactions
4. **External services just trigger**: Web servers, job runners, and UIs invoke transactions - they don't implement business logic

### Consequences

If your transactions are well-tested:
- **Deployment success + tests pass = system is healthy**
- Issues surfacing after deployment are "minor" (UI glitches, service integration) not "fundamental" (data corruption)
- You can reason about system correctness by examining transactions alone

## The Deploy Contract

A deploy is itself a transaction over the dataset, so `deploy.sql` refuses anything that would leave the database different from what the files say.

- **Two kinds of file.** Structure (tables, indexes, types, domains, policies, operators, constraint triggers) lives in run-once files written as plain DDL. Code (functions, views, registrations, catalog sync) lives in re-run files and is replaced on every deploy. A file holding the wrong kind stops the deploy before anything runs.
- **Run-once means fixed.** Editing, moving, re-sorting or removing a run-once file that already ran is refused, naming the file. The change goes in a new run-once file, so every database, old or new, reaches the same state by the same steps.
- **The database knows what it is.** It carries an environment label (`dev`, `test`, `prod`) and a deploy naming another is refused; relabelling is an explicit parameter. Files under `dev/` deploy only to dev databases.
- **Access is part of the state.** Privileges start closed, and grants, policies and triggers may not disappear unless a file declares the removal (below).
- **Reference data is a file.** `data/catalogs/<schema>.<table>.json` is the whole content of a lookup table; every deploy makes the table equal it.
- **Atomic head, then a tail.** Everything up to the `COMMIT` is one transaction. After it, `deploy.sql` builds concurrent indexes and fails on any invalid index.

The history of every deploy and script is readable in `internal.vw_deployment` and `internal.vw_deployed_script`. README "Extending the Framework" has the details.

## Layered Schema Architecture

The template has five application schemas (`internal`, `core`, `api`, `common`, `membership`) plus `extensions` for extension objects. Within them it uses a three-layer design:

- **API layer** (`api` schema): handler registrations. A handler shapes input and output and calls one kernel. A gateway calls only this layer.
- **Semantic layer** (`core` schema): views, kernels and problem functions. Kernels are the only writers.
- **Physical layer** (`core` schema): tables, keys, constraints, indexes and row-level security, in run-once files.

The [advanced template page](https://vvka-141.github.io/pgmi/docs/advanced/) draws the three layers with the sort-key bands.

### Physical Layer

**Focus**: Data integrity and performance

- Highly normalized table design (3NF or higher)
- Comprehensive constraints (NOT NULL, CHECK, UNIQUE, FK)
- Strategic indexing (B-tree, GIN, GiST, trigram)
- Partitioning for large tables
- No business logic - pure data modeling

```sql
-- Physical: one organization's rows, constrained, indexed, row-level secured
CREATE TABLE core.order (
    object_id          core.entity_id PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    organization_id    uuid NOT NULL,
    created_by_user_id uuid NOT NULL,
    status             text NOT NULL DEFAULT 'pending'
                       CONSTRAINT order_status_check CHECK (status IN ('pending', 'confirmed', 'shipped', 'delivered')),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT order_organization_object_key UNIQUE (organization_id, object_id)
);

CREATE TABLE core.order_line (
    object_id       core.entity_id PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    organization_id uuid NOT NULL,
    order_id        uuid NOT NULL,
    sku             text NOT NULL CONSTRAINT order_line_sku_check CHECK (btrim(sku) <> ''),
    quantity        int NOT NULL CONSTRAINT order_line_quantity_check CHECK (quantity > 0),
    unit_price      numeric(10,2) NOT NULL CONSTRAINT order_line_unit_price_check CHECK (unit_price >= 0),
    CONSTRAINT order_line_order_fkey FOREIGN KEY (organization_id, order_id)
        REFERENCES core.order (organization_id, object_id)
);

CREATE INDEX ix_order_line_order ON core.order_line (organization_id, order_id);

SELECT pg_temp.apply_entity_table_standards('core.order');
SELECT pg_temp.apply_entity_table_standards('core.order_line');
SELECT core.apply_org_rls('core.order');
SELECT core.apply_org_rls('core.order_line', p_has_created_by => false);
```

An order line names its order by `(organization_id, order_id)`, so it can never
point at another organization's order, and that pair leads an index. The
pgmi-endpoint skill walks through each line.

### Semantic Layer

**Focus**: Business rules and the only writes

- Kernels are the only writers: a handler or job never runs `INSERT`, `UPDATE` or `DELETE` on a table
- A kernel returns the whole row; one that must reach every organization is `SECURITY DEFINER`
- `*_problem` functions hold each rule once; the kernel and the handler both read them
- Views, created through `core.ensure_view`, are what handlers read

```sql
CREATE OR REPLACE FUNCTION core.fn_order_problem(p_order_id uuid, p_operation text)
RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN o.object_id IS NULL THEN 'core.order.not_found'
        WHEN p_operation = 'confirm' AND o.status <> 'pending' THEN 'core.order.not_pending'
    END
    FROM (SELECT p_order_id) AS k(id)
    LEFT JOIN core.order o ON o.object_id = k.id;
$$;

CREATE OR REPLACE FUNCTION core.fn_confirm_order(p_order_id uuid)
RETURNS core.order
LANGUAGE sql AS $$
    UPDATE core.order SET status = 'confirmed', updated_at = now()
    WHERE object_id = p_order_id AND status = 'pending'
    RETURNING *;
$$;
```

### API Layer

**Focus**: Protocol compliance and interface concerns

- A handler declares who may call it (`access` or `permissions`); the dispatcher refuses everyone else before the body runs
- The body reads its input, asks the problem function, calls one kernel and shapes the answer
- A refusal is a coded problem answer (404, 409, 422), never an exception

```sql
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', '3f6c2d1e-8a4b-4c7d-9e2f-1b5a6c7d8e9f',
        'name', 'confirm_order',
        'path', '/orders/{id:uuid}/confirm',
        'httpMethod', '^POST$',
        'access', 'organization',
        'outputSchema', '{"type": "object"}'::jsonb),
    $body$
DECLARE
    v_id      uuid := ((request).path_params->>'id')::uuid;
    v_problem text := core.fn_order_problem(v_id, 'confirm');
BEGIN
    IF v_problem IS NOT NULL THEN
        RETURN CASE v_problem
            WHEN 'core.order.not_found' THEN api.problem_response(404, 'Not Found', 'Order not found', code => 'core.order.not_found')
            WHEN 'core.order.not_pending' THEN api.problem_response(409, 'Conflict', 'The order is not pending', code => 'core.order.not_pending')
        END;
    END IF;
    IF (core.fn_confirm_order(v_id)).object_id IS NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'The order changed meanwhile; try again', code => 'core.order.changed');
    END IF;
    RETURN api.json_response(200, jsonb_build_object('orderId', v_id, 'status', 'confirmed'));
END;
    $body$);
```

### Why Layers Matter

1. **Separation of concerns**: Physical handles storage, semantic handles business rules and writes, API handles protocols
2. **Reusability**: REST, RPC and MCP handlers call the same kernels and views
3. **Testability**: Test each layer independently
4. **Maintainability**: Change API protocol without touching business logic

### Access Is Declared, Not Inherited

Layers are only a boundary if a role cannot reach past them. No routine is callable by `PUBLIC`: default privileges close every function the owner creates, and an end-of-deploy sweep closes the rest in every application schema the catalog shows. Only the template's roles may connect. Every grant, policy and trigger the database had before a deploy must still be there after it, unless a file declares the removal (`pg_temp.access_removal`). See README "Privileges: closed by default".

### Identity Is Resolved, Not Claimed

A request never states who calls. The transport signs the person in and opens a login session (`api.open_login_session`); after that a request carries only opaque handles (`x-login-session`, `x-organization-session`, or an MCP connection handle), or, for an MCP client with OAuth, the issuer, subject and client id the gateway verified in its token. The database resolves them, once per request, into one person, at most one organization and that membership's permissions, and only for a login in the api role. Handlers read the result through typed readers (`api.current_principal_id()`, `api.current_organization_id()`, `api.has_permission()`), never through a header or a setting. An MCP connection has its own agent principal: the request acts for the person (effective principal) and is made by the agent (`api.current_actual_principal_id()`, `api.current_actor_kind()` = `agent`), and every log records both. The agent holds its connection's `agent.*` role's permissions intersected with the person's, recomputed per request; it acts only while its connection is live (not paused, revoked or expired, its membership current, its organization's agents switched on). A handler declares `access` or `permissions`; the gateway refuses everyone else with 401 or 403 before the body runs, with one decision for REST, RPC and MCP. See README "Sign-in, sessions and connections".

## Testing Philosophy

**Every dataset transition must be tested.**

This is not optional. It's the foundation of system reliability.

### The Testing Principle

```
Deployment Success + All Tests Pass = System Is Healthy
```

If your transactional tests pass:
- Core data integrity is guaranteed
- Business rules are enforced
- State transitions work correctly

Any issues that surface afterward are **secondary concerns**:
- UI rendering bugs
- Service integration glitches
- Performance tuning needs

These are important but not fundamental - the dataset remains valid.

### What to Test

| Layer | Test Type | Example |
|-------|-----------|---------|
| Physical | Constraint validation | Duplicate email rejected |
| Semantic | Business logic | A kernel refuses to confirm a shipped order |
| API | Protocol compliance | 404 returned for missing resource |
| Integration | End-to-end flows | User registration creates profile and sends email |

### Test-First Development

1. **Envision the schema**: What tables, constraints, indexes?
2. **Design the transactions**: What operations modify the dataset?
3. **Write the tests**: How do we verify correctness?
4. **Implement**: Schema, functions, handlers
5. **Build triggers**: Web UI, background jobs, integrations

The tests define the contract. Implementation fulfills it.

## When to Choose This Template

Choose it when business rules belong in the database and its transactions, and your team writes PostgreSQL functions comfortably. [Choosing a template](https://vvka-141.github.io/pgmi/docs/templates/) compares it with the basic template.

## Advanced Patterns

### Background Jobs: Batches That Commit

Backlog work (retention, closing stale tasks, re-indexing documents) runs in
the database as a job a scheduler CALLs. The worker holds no logic: it opens a
session, sends one CALL and closes the session.

```sql
-- the batch: (int) RETURNS int, rows processed, 0 when done
CREATE FUNCTION api.job_close_stale_tasks_batch(p_batch_size int) RETURNS int ...;

-- the job: hands the batch to the driver
CREATE PROCEDURE api.job_close_stale_tasks(p_batch_size int DEFAULT 5000, p_max_batches int DEFAULT NULL)
LANGUAGE plpgsql AS $$
BEGIN
    CALL api.job_run('close_stale_tasks', 'api.job_close_stale_tasks_batch(int)', p_batch_size, p_max_batches);
END $$;
```

`api.job_run` is the one driver. It refuses bad arguments (22023) before any
lock, takes the job's session-level advisory lock (a second run returns
quietly), logs the run in `internal.job_run_log`, and commits after every
batch, so a long backlog never holds one long transaction or one long lock.
Batch functions select `FOR UPDATE SKIP LOCKED` so work they touch stays
available to the application.

**Why these rules.** A procedure may COMMIT only when it is CALLed at top
level, outside a transaction block, and has no `SECURITY DEFINER` and no `SET`
clause; the jobs follow from that. A session-level lock survives a rollback,
so a failed run keeps it until its session ends: the worker uses one session
per run, and the next run marks the failed one `abandoned`. The README's
Background jobs section has the contract, the run log and cron and `pg_cron`
examples.

### Near Real-Time Notifications

Use `LISTEN/NOTIFY` as a wake-up mechanism, not primary message delivery:

```sql
-- Trigger notifies on important events
CREATE FUNCTION core.notify_order_confirmed()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status = 'confirmed' AND OLD.status = 'pending' THEN
        PERFORM pg_notify('order_events', json_build_object(
            'event', 'order_confirmed',
            'order_id', NEW.object_id
        )::text);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
```

**Pattern:**
1. External service uses `LISTEN order_events` with timeout (long polling)
2. On notification: wake up, process event
3. On timeout: check for backlog anyway (resilience)
4. Primary delivery via persistent queue table, pg_notify is optimization

### Advanced Indexing

Leverage PostgreSQL's rich index ecosystem:

```sql
-- In a run-once file. pg_trgm is installed by deploy.sql in schema extensions.
-- Trigram index for search-as-you-type
CREATE INDEX ix_customer_name_trgm ON core.customer USING gin (name gin_trgm_ops);

-- Full-text search
ALTER TABLE core.product ADD COLUMN search_vector tsvector;
CREATE INDEX ix_product_search ON core.product USING gin (search_vector);

-- GiST for range queries
CREATE INDEX ix_event_duration ON core.event USING gist (tstzrange(start_at, end_at));
```

## Summary

The advanced template embodies a specific architectural philosophy:

1. **Application = Dataset**: PostgreSQL IS the application
2. **Transactions First**: Design state transitions before triggers
3. **Layered Architecture**: Physical → Semantic → API
4. **Test Everything**: All dataset transitions must be verified
5. **PostgreSQL Native**: Leverage the full power of PostgreSQL

This approach trades rapid prototyping for reliability, testability, and long-term maintainability. It is not for every project, but it fits systems where data integrity is a primary concern.
