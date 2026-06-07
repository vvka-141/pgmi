---
name: pgmi-system-design
description: "Application-as-dataset patterns, feature design"
user_invocable: false
---


## Purpose

Guide the design of systems using the "application as dataset" paradigm. This skill covers how to architect, design, and implement applications where PostgreSQL IS the application, not just storage.

## When to Use

- When designing new features for pgmi-based applications
- When helping users plan their application architecture
- When evaluating whether pgmi approach fits a use case
- When implementing transactions, handlers, and tests
- When advising on schema design and layering

## Core Paradigm: Application as Dataset

### The Mental Model

```
Traditional:    Application Code  →  Database (storage)
Dataset-centric: Database (application) ← Triggers (UI, jobs, APIs)
```

In dataset-centric architecture:
- **The database IS the application** - all state and business logic lives in PostgreSQL
- **External actors trigger transactions** - REST/RPC/MCP/jobs invoke well-defined operations
- **Transactions guarantee validity** - every state change is atomic and validated
- **If tests pass, the system works** - external issues become secondary concerns

### Why This Matters

| Aspect | Traditional | Dataset-Centric |
|--------|-------------|-----------------|
| State location | Distributed (app servers, caches, DB) | Single (PostgreSQL) |
| Consistency | Eventual, complex coordination | Immediate, transactional |
| Testing | Mock dependencies, integration tests | Transactional tests against real DB |
| Debugging | Trace across services | Query the database |
| Correctness | Hope services coordinate properly | Proven by transaction tests |

### When This Approach Fits

**Strong fit:**
- Data integrity is critical (financial, healthcare, compliance)
- Complex business rules with many edge cases
- Audit requirements (who changed what, when)
- Team has strong PostgreSQL skills
- Concurrent access patterns benefit from ACID

**Weak fit:**
- Simple CRUD with minimal business logic
- Team unfamiliar with PostgreSQL
- High write throughput requiring sharding
- Primarily read-heavy analytics (consider data warehouse)

## Design Methodology

### The Design Sequence

```
1. Schema Design     → What data do we store? What constraints ensure validity?
2. Transaction Design → What operations modify the data? What are the rules?
3. Test Design       → How do we verify each transaction works correctly?
4. Implementation    → Build schema, transactions, tests
5. Trigger Design    → How do users/systems invoke these transactions?
```

**Key insight**: Steps 1-4 happen entirely in PostgreSQL. Step 5 (triggers) is secondary.

### Step 1: Schema Design (Physical Layer)

**Goal**: Model the data with integrity constraints that make invalid states unrepresentable.

**Questions to ask:**
- What entities exist in this domain?
- What are the relationships between entities?
- What constraints prevent invalid data?
- What indexes support our query patterns?

**Principles:**
- Normalize aggressively (3NF minimum)
- Use constraints liberally (NOT NULL, CHECK, UNIQUE, FK)
- Choose appropriate types (UUID vs serial, timestamptz vs timestamp)
- Plan for soft-delete if needed (deleted_at pattern)

**Example:**
```sql
-- Physical layer: normalized, constrained
CREATE TABLE core.account (
    account_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'closed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE core.transaction (
    transaction_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id UUID NOT NULL REFERENCES core.account(account_id),
    amount NUMERIC(15,2) NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('credit', 'debit')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Constraint: debits cannot exceed balance
-- (Implemented via trigger or check in transaction function)
```

### Step 2: Transaction Design (Virtual Layer)

**Goal**: Define every operation that modifies the dataset as a well-defined transaction.

**Questions to ask:**
- What operations can users/systems perform?
- What preconditions must be met?
- What postconditions must hold?
- What errors can occur?

**Principles:**
- Each transaction is a function or procedure
- Transactions validate preconditions and raise exceptions on failure
- Transactions are atomic (all-or-nothing)
- Name transactions by what they DO (verbs): `create_account`, `transfer_funds`, `suspend_account`

**Example:**
```sql
-- Virtual layer: business operations
CREATE FUNCTION core.transfer_funds(
    p_from_account UUID,
    p_to_account UUID,
    p_amount NUMERIC
)
RETURNS UUID  -- Returns transaction_id
LANGUAGE plpgsql
AS $$
DECLARE
    v_balance NUMERIC;
    v_txn_id UUID;
BEGIN
    -- Precondition: positive amount
    IF p_amount <= 0 THEN
        RAISE EXCEPTION 'Amount must be positive: %', p_amount;
    END IF;

    -- Precondition: sufficient balance
    SELECT COALESCE(SUM(CASE type WHEN 'credit' THEN amount ELSE -amount END), 0)
    INTO v_balance
    FROM core.transaction
    WHERE account_id = p_from_account;

    IF v_balance < p_amount THEN
        RAISE EXCEPTION 'Insufficient balance: % < %', v_balance, p_amount;
    END IF;

    -- Precondition: accounts exist and are active
    IF NOT EXISTS (SELECT 1 FROM core.account WHERE account_id = p_from_account AND status = 'active') THEN
        RAISE EXCEPTION 'Source account not found or not active: %', p_from_account;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM core.account WHERE account_id = p_to_account AND status = 'active') THEN
        RAISE EXCEPTION 'Destination account not found or not active: %', p_to_account;
    END IF;

    -- Execute transfer
    INSERT INTO core.transaction (account_id, amount, type)
    VALUES (p_from_account, p_amount, 'debit');

    INSERT INTO core.transaction (account_id, amount, type)
    VALUES (p_to_account, p_amount, 'credit')
    RETURNING transaction_id INTO v_txn_id;

    RETURN v_txn_id;
END;
$$;
```

### Step 3: Test Design

**Goal**: Verify every transaction behaves correctly for all scenarios.

**Questions to ask:**
- What happens with valid input? (happy path)
- What happens with invalid input? (validation errors)
- What happens with edge cases? (zero, null, boundary values)
- What happens with concurrent access? (if relevant)

**Principles:**
- Test precondition enforcement (invalid inputs rejected)
- Test postconditions (state after transaction is correct)
- Test idempotency where applicable
- Use RAISE EXCEPTION for failures (fail-fast)

**Example:**
```sql
-- __test__/test_transfer_funds.sql
DO $$
DECLARE
    v_account_a UUID;
    v_account_b UUID;
    v_txn_id UUID;
    v_balance NUMERIC;
BEGIN
    -- Setup: Create two accounts with initial balance
    INSERT INTO core.account (email) VALUES ('a@test.com') RETURNING account_id INTO v_account_a;
    INSERT INTO core.account (email) VALUES ('b@test.com') RETURNING account_id INTO v_account_b;
    INSERT INTO core.transaction (account_id, amount, type) VALUES (v_account_a, 1000, 'credit');

    -- Test: Successful transfer
    v_txn_id := core.transfer_funds(v_account_a, v_account_b, 100);
    IF v_txn_id IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: transfer_funds returned NULL';
    END IF;

    -- Verify balances
    SELECT COALESCE(SUM(CASE type WHEN 'credit' THEN amount ELSE -amount END), 0)
    INTO v_balance FROM core.transaction WHERE account_id = v_account_a;
    IF v_balance != 900 THEN
        RAISE EXCEPTION 'TEST FAILED: Expected balance 900, got %', v_balance;
    END IF;

    RAISE NOTICE '✓ Successful transfer works';

    -- Test: Insufficient balance rejected
    BEGIN
        PERFORM core.transfer_funds(v_account_a, v_account_b, 10000);
        RAISE EXCEPTION 'TEST FAILED: Should have rejected insufficient balance';
    EXCEPTION WHEN OTHERS THEN
        IF SQLERRM NOT LIKE '%Insufficient balance%' THEN
            RAISE EXCEPTION 'TEST FAILED: Wrong error message: %', SQLERRM;
        END IF;
    END;

    RAISE NOTICE '✓ Insufficient balance rejected';

    -- Test: Negative amount rejected
    BEGIN
        PERFORM core.transfer_funds(v_account_a, v_account_b, -50);
        RAISE EXCEPTION 'TEST FAILED: Should have rejected negative amount';
    EXCEPTION WHEN OTHERS THEN
        IF SQLERRM NOT LIKE '%must be positive%' THEN
            RAISE EXCEPTION 'TEST FAILED: Wrong error message: %', SQLERRM;
        END IF;
    END;

    RAISE NOTICE '✓ Negative amount rejected';
END $$;
```

### Step 4: Implementation

With design complete, implementation is straightforward:

1. Create schema (physical layer tables)
2. Create views for common queries (virtual layer)
3. Create transaction functions (virtual layer)
4. Create tests (verify everything)
5. Deploy and run tests

### Step 5: Trigger Design (API Layer)

**Goal**: Expose transactions to external actors via appropriate protocols.

**Questions to ask:**
- Who invokes this transaction? (users, jobs, AI agents)
- What protocol fits? (REST for web, RPC for services, MCP for AI)
- What authentication is required?
- What response format is expected?

**Example:**
```sql
-- API layer: REST handler
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'transfer-funds-handler',
        'uri', '^/transfers$',
        'httpMethod', '^POST$',
        'name', 'transfer_funds',
        'requiresAuth', true
    ),
    $body$
DECLARE
    v_body jsonb;
    v_txn_id UUID;
BEGIN
    v_body := api.content_json((request).content);

    -- Call the transaction (virtual layer)
    v_txn_id := core.transfer_funds(
        (v_body->>'fromAccount')::uuid,
        (v_body->>'toAccount')::uuid,
        (v_body->>'amount')::numeric
    );

    RETURN api.json_response(201, jsonb_build_object(
        'transactionId', v_txn_id,
        'message', 'Transfer completed'
    ));
EXCEPTION WHEN OTHERS THEN
    -- Transaction errors become 400 Bad Request
    RETURN api.problem_response(400, 'Transfer failed', SQLERRM);
END;
    $body$
);
```

## Advanced Patterns

### Views for Business Queries (Virtual Layer)

Create views that provide business-aligned data access:

```sql
-- Virtual layer: denormalized for business queries
CREATE VIEW core.account_summary AS
SELECT
    a.account_id,
    a.email,
    a.status,
    COALESCE(SUM(CASE t.type WHEN 'credit' THEN t.amount ELSE -t.amount END), 0) AS balance,
    COUNT(t.transaction_id) AS transaction_count,
    MAX(t.created_at) AS last_transaction_at
FROM core.account a
LEFT JOIN core.transaction t USING (account_id)
GROUP BY a.account_id, a.email, a.status;

-- Handlers query the view, not raw tables
SELECT * FROM core.account_summary WHERE account_id = $1;
```

### Stored Procedures for Batch Operations

Use procedures for operations that process multiple items with per-item commits:

```sql
CREATE PROCEDURE core.process_pending_transfers()
LANGUAGE plpgsql AS $$
DECLARE
    v_pending RECORD;
    v_processed INT := 0;
BEGIN
    FOR v_pending IN
        SELECT * FROM core.pending_transfer
        WHERE status = 'pending'
        ORDER BY created_at
        FOR UPDATE SKIP LOCKED
    LOOP
        BEGIN
            PERFORM core.transfer_funds(
                v_pending.from_account,
                v_pending.to_account,
                v_pending.amount
            );
            UPDATE core.pending_transfer SET status = 'completed' WHERE id = v_pending.id;
            v_processed := v_processed + 1;
        EXCEPTION WHEN OTHERS THEN
            UPDATE core.pending_transfer SET status = 'failed', error = SQLERRM WHERE id = v_pending.id;
        END;

        -- Commit each transfer independently
        COMMIT;
    END LOOP;

    RAISE NOTICE 'Processed % transfers', v_processed;
END;
$$;
```

### Event Notifications (pg_notify)

Use LISTEN/NOTIFY for near real-time wake-up signals:

```sql
-- Trigger to notify on important events
CREATE FUNCTION core.notify_transfer_completed()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('transfers', json_build_object(
        'event', 'completed',
        'transaction_id', NEW.transaction_id,
        'account_id', NEW.account_id
    )::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_notify_transfer
AFTER INSERT ON core.transaction
FOR EACH ROW EXECUTE FUNCTION core.notify_transfer_completed();
```

**Pattern for consumers:**
1. `LISTEN transfers` with timeout (long polling)
2. On notification: wake up, process event
3. On timeout: check for backlog anyway (resilience)

## Design Checklist

When designing a new feature:

- [ ] **Schema**: What tables/constraints model this feature?
- [ ] **Transactions**: What operations modify data? What are preconditions?
- [ ] **Tests**: How do we verify each transaction?
- [ ] **Views**: What queries does this feature need?
- [ ] **Handlers**: What protocols expose this feature?
- [ ] **Notifications**: Does anything need real-time updates?

## Common Mistakes

### Mistake 1: Business Logic in Handlers

```sql
-- ❌ BAD: Logic in API layer
SELECT api.create_or_replace_rest_handler(...,
    $body$
    BEGIN
        -- Business logic here
        IF balance < amount THEN ...
        INSERT INTO transaction ...
    END;
    $body$
);

-- ✅ GOOD: Handler calls virtual layer
SELECT api.create_or_replace_rest_handler(...,
    $body$
    BEGIN
        PERFORM core.transfer_funds(...);  -- Virtual layer has logic
    END;
    $body$
);
```

### Mistake 2: Skipping Tests

```sql
-- ❌ BAD: "It's just a simple function"
-- (No tests written)

-- ✅ GOOD: Every transaction has tests
-- __test__/test_transfer_funds.sql exists and covers:
-- - Happy path
-- - Validation errors
-- - Edge cases
```

### Mistake 3: Raw Table Access in Handlers

```sql
-- ❌ BAD: Direct table access
SELECT * FROM core.account WHERE account_id = $1;

-- ✅ GOOD: Use virtual layer views
SELECT * FROM core.account_summary WHERE account_id = $1;
```

### Mistake 4: Designing Triggers First

```sql
-- ❌ BAD: Start with REST API design
-- "We need POST /transfers endpoint"

-- ✅ GOOD: Start with transaction design
-- "We need transfer_funds(from, to, amount) transaction"
```

## Integration with Other Skills

- **Uses**: pgmi-philosophy.md (execution fabric principles)
- **Implements**: pgmi-test-architecture.md (testing patterns)
- **Extends**: pgmi-api-architecture.md (protocol design)
- **Requires**: pgmi-sql.md (SQL coding standards)

