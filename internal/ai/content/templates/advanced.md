# Architecture: Application as Dataset

This document describes the architectural philosophy behind the advanced template. Understanding these principles is essential for building robust, testable, and maintainable systems with pgmi.

## Core Philosophy

**Your application IS the PostgreSQL database.**

In this paradigm:
- The database is not "storage for your application" - it IS the application
- All application state lives in PostgreSQL as a carefully modeled dataset
- External actors (users, background jobs, AI agents) modify the dataset through well-defined transactions
- REST, RPC, and MCP endpoints are merely **trigger mechanisms** for these transactions
- Services outside PostgreSQL (web servers, job runners, UIs) are secondary - they invoke transactions but don't own business logic

### Why This Matters

Traditional architecture:
```
User → Web Server → Business Logic → ORM → Database
                         ↑
                   (complexity lives here)
```

Dataset-centric architecture:
```
User → Gateway → PostgreSQL Transaction
                        ↑
                  (complexity lives here)
```

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

## Layered Schema Architecture

The template uses a three-layer design within the four-schema structure:

```
┌─────────────────────────────────────────────────────────────┐
│                      API Layer (api schema)                  │
│  HTTP handlers, protocol compliance, request/response        │
│  Calls virtual layer, focuses on interface concerns          │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                   Virtual Layer (core schema)                │
│  Views, functions, stored procedures                         │
│  Business-aligned queries, reusable across handlers          │
│  Window functions, CTEs, rollups, aggregations               │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                  Physical Layer (core schema)                │
│  Normalized tables with constraints                          │
│  Focused on data integrity and query performance             │
│  Indexes, partitioning, foreign keys                         │
└─────────────────────────────────────────────────────────────┘
```

### Physical Layer

**Focus**: Data integrity and performance

- Highly normalized table design (3NF or higher)
- Comprehensive constraints (NOT NULL, CHECK, UNIQUE, FK)
- Strategic indexing (B-tree, GIN, GiST, trigram)
- Partitioning for large tables
- No business logic - pure data modeling

```sql
-- Physical: Normalized, constrained, indexed
CREATE TABLE core.order (
    order_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id UUID NOT NULL REFERENCES core.customer(customer_id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'confirmed', 'shipped', 'delivered')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE core.order_line (
    order_line_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL REFERENCES core.order(order_id),
    product_id UUID NOT NULL REFERENCES core.product(product_id),
    quantity INT NOT NULL CHECK (quantity > 0),
    unit_price NUMERIC(10,2) NOT NULL CHECK (unit_price >= 0)
);
```

### Virtual Layer

**Focus**: Business-aligned data access

- Views that denormalize for business queries
- Functions that encapsulate business operations
- Reusable across multiple API handlers
- Complex queries (window functions, CTEs, rollups)

```sql
-- Virtual: Denormalized view for business queries
CREATE VIEW core.order_summary AS
SELECT
    o.order_id,
    o.customer_id,
    c.email AS customer_email,
    o.status,
    COUNT(ol.order_line_id) AS line_count,
    SUM(ol.quantity * ol.unit_price) AS total_amount,
    o.created_at,
    o.updated_at
FROM core.order o
JOIN core.customer c USING (customer_id)
LEFT JOIN core.order_line ol USING (order_id)
GROUP BY o.order_id, o.customer_id, c.email, o.status, o.created_at, o.updated_at;

-- Virtual: Business operation
CREATE FUNCTION core.confirm_order(p_order_id UUID)
RETURNS void AS $$
BEGIN
    UPDATE core.order
    SET status = 'confirmed', updated_at = now()
    WHERE order_id = p_order_id AND status = 'pending';

    IF NOT FOUND THEN
        RAISE EXCEPTION 'Order % not found or not pending', p_order_id;
    END IF;
END;
$$ LANGUAGE plpgsql;
```

### API Layer

**Focus**: Protocol compliance and interface concerns

- HTTP handlers call virtual layer functions
- JSON schema validation
- Status code mapping
- Authentication enforcement
- Minimal business logic

```sql
-- API: Protocol-focused handler
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'confirm-order-handler-uuid',
        'uri', '^/orders/([0-9a-f-]+)/confirm$',
        'httpMethod', '^POST$',
        'name', 'confirm_order',
        'requiresAuth', true
    ),
    $body$
DECLARE
    v_order_id UUID;
BEGIN
    -- Extract and validate
    v_order_id := (regexp_matches((request).url, '/orders/([0-9a-f-]+)/confirm'))[1]::uuid;

    -- Call virtual layer
    PERFORM core.confirm_order(v_order_id);

    -- Return protocol-compliant response
    RETURN api.json_response(200, jsonb_build_object(
        'message', 'Order confirmed',
        'orderId', v_order_id
    ));
END;
    $body$
);
```

### Why Layers Matter

1. **Separation of concerns**: Physical handles storage, virtual handles business, API handles protocols
2. **Reusability**: Multiple API handlers can use the same virtual layer views/functions
3. **Testability**: Test each layer independently
4. **Maintainability**: Change API protocol without touching business logic

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
| Virtual | Business logic | Order total calculated correctly |
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

### Choose Advanced Template If:

- You believe business logic belongs in the database
- You want transactional guarantees for all state changes
- You need multi-protocol support (REST, RPC, MCP)
- Your team has strong PostgreSQL skills
- You value testability over rapid prototyping
- You're building systems where data integrity is critical

### Prerequisites

- Solid PostgreSQL knowledge (functions, views, transactions)
- Understanding of SQL vs PL/pgSQL trade-offs
- Familiarity with HTTP semantics (if using REST)
- Comfort with pgmi's deployment model

### Maybe Choose Basic Template If:

- Learning pgmi for the first time
- Simple migration-only use case
- Team is new to PostgreSQL
- Rapid prototyping is the priority

## Advanced Patterns

### Stored Procedures for Batch Processing

Reduce client-server roundtrips by processing batches server-side:

```sql
CREATE PROCEDURE core.process_pending_orders(p_batch_size INT DEFAULT 100)
LANGUAGE plpgsql AS $$
DECLARE
    v_processed INT := 0;
    v_order RECORD;
BEGIN
    LOOP
        -- Process one batch
        FOR v_order IN
            SELECT order_id FROM core.order
            WHERE status = 'pending'
            ORDER BY created_at
            LIMIT p_batch_size
            FOR UPDATE SKIP LOCKED
        LOOP
            PERFORM core.confirm_order(v_order.order_id);
            v_processed := v_processed + 1;
        END LOOP;

        -- Commit this batch
        COMMIT;

        -- Exit if no more work
        EXIT WHEN v_processed = 0;
        v_processed := 0;
    END LOOP;
END;
$$;
```

**Benefits:**
- Single connection processes entire backlog
- Commits per batch limit transaction size
- `SKIP LOCKED` enables parallel workers
- No client involvement during processing

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
            'order_id', NEW.order_id
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
-- Trigram index for search-as-you-type
CREATE EXTENSION IF NOT EXISTS pg_trgm;
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
3. **Layered Architecture**: Physical → Virtual → API
4. **Test Everything**: All dataset transitions must be verified
5. **PostgreSQL Native**: Leverage the full power of PostgreSQL

This approach trades rapid prototyping for reliability, testability, and long-term maintainability. It's not for every project, but for systems where data integrity matters, it's exceptionally powerful.
