# Database Testing with pgmi

This guide teaches you how to test your PostgreSQL code using pgmi — from your first test to hierarchical fixtures. Every example is copy-paste ready.

---

## The problem pgmi solves

Testing database code is hard because **changes persist**. If a test creates a table, that table exists for the next test. If a test inserts rows, those rows are visible to every test that follows. Tests become order-dependent, flaky, and impossible to run in isolation.

Most teams solve this with cleanup scripts — `DELETE FROM`, `DROP TABLE IF EXISTS`, teardown hooks. This is fragile. Miss one cleanup step and your test suite breaks in subtle, hard-to-debug ways.

pgmi takes a different approach: **your tests never touch the real database at all.**

---

## How it works (the short version)

When you run `pgmi test`, pgmi:

1. Opens a transaction
2. Runs your fixtures and tests inside that transaction
3. Rolls the entire transaction back when done

Nothing persists. Your database is identical before and after the test run. Every time.

Inside that transaction, pgmi uses PostgreSQL **savepoints** to isolate each test from every other test. Each test gets a clean copy of the fixture state, regardless of what previous tests did.

You don't manage any of this. You write SQL. pgmi handles the rest.

---

## Your first test

Starting from a project created with `pgmi init myapp --template basic`, let's say your `migrations/001_initial.sql` creates a `users` table:

```sql
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT
);
```

### Step 1: Create a test directory

Create a `__test__` folder inside `migrations/`:

```
myapp/
├── deploy.sql
├── pgmi.yaml
└── migrations/
    ├── 001_initial.sql
    └── __test__/
        └── test_users_table.sql
```

The name `__test__` (or `__tests__`) is special. pgmi automatically finds these directories and treats everything inside them as test code. Test files are **physically separated** from deployment files — they cannot accidentally run during a real deployment.

### Step 2: Write the test

Create `migrations/__test__/test_users_table.sql`:

```sql
DO $$
BEGIN
    -- Insert a test user
    INSERT INTO users (name, email) VALUES ('Alice', 'alice@example.com');

    -- Verify the user exists
    IF NOT EXISTS (SELECT 1 FROM users WHERE name = 'Alice') THEN
        RAISE EXCEPTION 'TEST FAILED: User was not inserted';
    END IF;

    -- Verify the email column works
    IF (SELECT email FROM users WHERE name = 'Alice') != 'alice@example.com' THEN
        RAISE EXCEPTION 'TEST FAILED: Email does not match';
    END IF;

    RAISE NOTICE 'PASS: users table works correctly';
END $$;
```

Tests are plain SQL. If something is wrong, `RAISE EXCEPTION` stops execution immediately with a clear message. If everything is fine, the test completes silently (or with a `RAISE NOTICE` for your own visibility).

### Step 3: Deploy, then test

```bash
# First, deploy your schema
pgmi deploy . --overwrite --force

# Then, run tests
pgmi test .
```

You should see:

```
PASS: users table works correctly
All tests passed.
```

### Step 4: Check your database

Open pgAdmin or run:

```bash
psql -h localhost -U postgres -d myapp -c "SELECT * FROM users;"
```

```
 id | name | email
----+------+-------
(0 rows)
```

**Zero rows.** The test inserted a user, verified it, and pgmi rolled everything back. Your database is untouched.

---

## Fixtures: shared setup for multiple tests

When you have multiple tests that all need the same starting data, you don't want to repeat the setup in every test file. That's what **fixtures** are for.

A fixture is a file named `_setup.sql` inside a `__test__` directory. pgmi runs it before the tests in that directory, and every test sees the same fixture state — even if a previous test modified or deleted the data.

### Example

```
migrations/
├── 001_initial.sql          ← Creates the users table
└── __test__/
    ├── _setup.sql            ← Inserts test data (the fixture)
    ├── test_insert.sql       ← Tests inserting a new user
    └── test_count.sql        ← Tests counting users
```

**`_setup.sql`** — the fixture:

```sql
INSERT INTO users (name, email) VALUES
    ('Alice', 'alice@example.com'),
    ('Bob', 'bob@example.com');
```

**`test_insert.sql`**:

```sql
DO $$
DECLARE
    v_count INT;
BEGIN
    -- Fixture gave us 2 users. Insert a third.
    INSERT INTO users (name, email) VALUES ('Charlie', 'charlie@example.com');

    SELECT COUNT(*) INTO v_count FROM users;
    IF v_count != 3 THEN
        RAISE EXCEPTION 'TEST FAILED: Expected 3 users, got %', v_count;
    END IF;

    RAISE NOTICE 'PASS: insert works (3 users after insert)';
END $$;
```

**`test_count.sql`**:

```sql
DO $$
DECLARE
    v_count INT;
BEGIN
    -- This test also sees exactly 2 users from the fixture.
    -- Charlie from test_insert.sql is NOT here — that test was rolled back.
    SELECT COUNT(*) INTO v_count FROM users;
    IF v_count != 2 THEN
        RAISE EXCEPTION 'TEST FAILED: Expected 2 users from fixture, got %', v_count;
    END IF;

    RAISE NOTICE 'PASS: fixture provides exactly 2 users';
END $$;
```

Run it:

```bash
pgmi test .
```

```
PASS: insert works (3 users after insert)
PASS: fixture provides exactly 2 users
All tests passed.
```

Both tests pass. `test_count.sql` sees exactly 2 users even though `test_insert.sql` added a third. pgmi rolled back `test_insert.sql` before running `test_count.sql`.

**This is the core guarantee: every test starts from the exact fixture state, no matter what.**

---

## What happens under the hood

Here's the exact sequence pgmi executes for the example above. Understanding this is optional, but it explains why the guarantee works.

```
BEGIN;                                  ← outer transaction (pgmi opens this)

    SAVEPOINT sp_setup_root;            ← fixture boundary
    INSERT INTO users ... (fixture);    ← _setup.sql runs

        SAVEPOINT sp_test_1;            ← test boundary
        ... test_insert.sql runs ...
        ROLLBACK TO sp_test_1;          ← undo test_insert.sql changes

        SAVEPOINT sp_test_2;            ← test boundary
        ... test_count.sql runs ...
        ROLLBACK TO sp_test_2;          ← undo test_count.sql changes

    ROLLBACK TO sp_setup_root;          ← undo fixture (Alice, Bob gone)

ROLLBACK;                               ← undo everything (pgmi closes this)
```

The `ROLLBACK TO sp_test_1` after `test_insert.sql` is what erases Charlie. The database state returns to exactly what `_setup.sql` created. Then `test_count.sql` runs against that clean state.

The `ROLLBACK TO sp_setup_root` at the end erases even the fixture data. And the final `ROLLBACK` undoes everything else — including any schema changes your deployment created.

**PostgreSQL's transactional savepoints do all the work.** pgmi just generates the right savepoint structure. No cleanup scripts. No teardown hooks. No manual state management.

---

## Hierarchical fixtures

Real projects have groups of related tests, each needing different base data. pgmi supports this with nested `__test__` directories where each level adds its own fixture.

### Example: an e-commerce project

```
migrations/
├── 001_schema.sql
└── __test__/
    ├── _setup.sql                    ← Base fixture: creates 2 users
    ├── test_user_count.sql
    │
    ├── orders/
    │   ├── _setup.sql                ← Adds orders for the 2 users
    │   ├── test_order_total.sql
    │   └── test_order_status.sql
    │
    └── admin/
        ├── _setup.sql                ← Adds an admin role
        └── test_admin_access.sql
```

**Root `_setup.sql`**:
```sql
INSERT INTO users (name, email) VALUES
    ('Alice', 'alice@example.com'),
    ('Bob', 'bob@example.com');
```

**`orders/_setup.sql`**:
```sql
-- This runs AFTER root _setup.sql.
-- Alice and Bob already exist.
INSERT INTO orders (user_id, total, status) VALUES
    (1, 99.99, 'pending'),
    (2, 149.50, 'shipped');
```

**`admin/_setup.sql`**:
```sql
-- This also runs after root _setup.sql.
-- Alice and Bob exist, but NO orders (orders fixture is separate).
INSERT INTO user_roles (user_id, role) VALUES (1, 'admin');
```

pgmi executes this structure as nested savepoints:

```
BEGIN;

    SAVEPOINT sp_root_setup;                ← Root fixture (Alice, Bob)
    ... _setup.sql ...

        SAVEPOINT sp_test_user_count;       ← Test against root fixture
        ... test_user_count.sql ...
        ROLLBACK TO sp_test_user_count;

        SAVEPOINT sp_orders_setup;          ← Orders fixture (adds orders)
        ... orders/_setup.sql ...

            SAVEPOINT sp_test_order_total;
            ... test_order_total.sql ...
            ROLLBACK TO sp_test_order_total;

            SAVEPOINT sp_test_order_status;
            ... test_order_status.sql ...
            ROLLBACK TO sp_test_order_status;

        ROLLBACK TO sp_orders_setup;        ← Undo orders fixture

        SAVEPOINT sp_admin_setup;           ← Admin fixture (adds role)
        ... admin/_setup.sql ...

            SAVEPOINT sp_test_admin_access;
            ... test_admin_access.sql ...
            ROLLBACK TO sp_test_admin_access;

        ROLLBACK TO sp_admin_setup;         ← Undo admin fixture

    ROLLBACK TO sp_root_setup;              ← Undo root fixture

ROLLBACK;
```

**What each test sees:**

| Test | Users | Orders | Admin role |
|------|-------|--------|------------|
| `test_user_count.sql` | Alice, Bob | none | none |
| `test_order_total.sql` | Alice, Bob | 2 orders | none |
| `test_order_status.sql` | Alice, Bob | 2 orders | none |
| `test_admin_access.sql` | Alice, Bob | none | Alice is admin |

Each subdirectory gets its parent's fixture plus its own. Tests in `orders/` see users and orders but no admin role. Tests in `admin/` see users and the admin role but no orders. The fixtures compose, and each level is fully isolated.

---

## Filtering tests

You don't have to run everything every time:

```bash
# Run only order-related tests
pgmi test . --filter "/orders/"

# Run only a specific test file
pgmi test . --filter "test_admin_access"

# List all tests without running them
pgmi test . --list
```

When you filter, pgmi automatically includes all ancestor fixtures. If you run `--filter "/orders/"`, pgmi still runs the root `_setup.sql` (because `orders/_setup.sql` depends on it) — you don't need to think about this.

---

## Writing effective tests

### The pattern

Every test follows the same shape:

```sql
DO $$
BEGIN
    -- 1. Do something (or not — test the existing fixture state)
    -- 2. Check the result
    -- 3. RAISE EXCEPTION if wrong, RAISE NOTICE if right
END $$;
```

### Testing a function

```sql
DO $$
DECLARE
    v_result BOOLEAN;
BEGIN
    v_result := validate_email('user@example.com');
    IF NOT v_result THEN
        RAISE EXCEPTION 'Valid email was rejected';
    END IF;

    v_result := validate_email('not-an-email');
    IF v_result THEN
        RAISE EXCEPTION 'Invalid email was accepted';
    END IF;

    RAISE NOTICE 'PASS: email validation';
END $$;
```

### Testing an expected error

```sql
DO $$
BEGIN
    -- This should fail with a constraint violation
    BEGIN
        INSERT INTO users (name, email) VALUES (NULL, 'test@example.com');
        RAISE EXCEPTION 'TEST FAILED: NULL name was accepted (should violate NOT NULL)';
    EXCEPTION
        WHEN not_null_violation THEN
            RAISE NOTICE 'PASS: NOT NULL constraint on name works';
    END;
END $$;
```

### Testing with data from the fixture

```sql
DO $$
DECLARE
    v_total NUMERIC;
BEGIN
    -- Fixture already inserted orders. Just query and verify.
    SELECT SUM(total) INTO v_total FROM orders;
    IF v_total != 249.49 THEN
        RAISE EXCEPTION 'TEST FAILED: Expected total 249.49, got %', v_total;
    END IF;

    RAISE NOTICE 'PASS: order totals sum correctly';
END $$;
```

---

## What you don't need to do

Because pgmi manages the transaction lifecycle, you skip the entire category of problems that make database testing painful:

| Traditional approach | With pgmi |
|---------------------|-----------|
| Write `teardown.sql` to clean up after tests | Not needed — savepoint rollback handles it |
| Worry about test execution order | Not needed — each test starts from fixture state |
| Manage test database separately | Not needed — tests run against your real deployment, then vanish |
| Build a test runner or assertion framework | Not needed — `RAISE EXCEPTION` is the assertion |
| Reset sequences, truncate tables, drop temp objects | Not needed — outer `ROLLBACK` erases everything |

---

## The gated deployment pattern

So far, you've been running tests *after* deployment with `pgmi test`. But pgmi can also run tests **inside** the deployment itself — so that if any test fails, the entire deployment rolls back and your database is unchanged.

To understand how this works, you need to know one thing about `deploy.sql`: **it doesn't execute SQL directly. It builds a plan.**

### Why deploy.sql builds a plan

When pgmi runs `deploy.sql`, your script doesn't execute migrations immediately. Instead, it schedules commands into a queue (`pg_temp.pgmi_plan`). After `deploy.sql` finishes, pgmi reads that queue and executes it step by step.

This is why `deploy.sql` uses `pgmi_plan_*` functions instead of running SQL directly:

```sql
-- This does NOT run BEGIN immediately.
-- It adds "BEGIN;" to the execution queue.
PERFORM pg_temp.pgmi_plan_command('BEGIN;');
```

Think of it like writing a recipe vs. cooking. `deploy.sql` writes the recipe. pgmi follows it afterward.

Here are the functions you'll use:

| Function | What it schedules |
|----------|-------------------|
| `pgmi_plan_command('SQL')` | A raw SQL statement (e.g., `BEGIN;`, `COMMIT;`) |
| `pgmi_plan_file('./path/to/file.sql')` | The contents of a project file |
| `pgmi_plan_tests()` | All tests with their savepoints and rollbacks |

### Why this matters for testing

`pgmi_plan_tests()` is the key. When you call it inside `deploy.sql`, pgmi generates the entire savepoint structure you saw in the earlier sections — every fixture setup, every test wrapped in its own savepoint, every rollback — and inserts all of it into the execution queue. You don't manage savepoints yourself. You just call `pgmi_plan_tests()` and pgmi takes care of the rest.

### Putting it together

Here's a `deploy.sql` that deploys your schema and gates the commit on tests passing:

```sql
DO $$
DECLARE
    v_file RECORD;
BEGIN
    -- Schedule: start a transaction
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    -- Schedule: execute each migration file
    FOR v_file IN (
        SELECT path FROM pg_temp.pgmi_source
        WHERE is_sql_file ORDER BY path
    )
    LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;

    -- Schedule: run all tests (with automatic savepoints)
    PERFORM pg_temp.pgmi_plan_tests();

    -- Schedule: commit only if everything above succeeded
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
```

After `deploy.sql` finishes, the execution queue looks like this:

```
1.  BEGIN;
2.  <contents of 001_initial.sql>
3.  <contents of 002_add_email.sql>
4.  SAVEPOINT sp_setup_root;           ┐
5.  <_setup.sql contents>              │
6.  SAVEPOINT sp_test_1;               │
7.  <test_insert.sql contents>         │  generated by
8.  ROLLBACK TO sp_test_1;             │  pgmi_plan_tests()
9.  SAVEPOINT sp_test_2;               │
10. <test_count.sql contents>          │
11. ROLLBACK TO sp_test_2;             │
12. ROLLBACK TO sp_setup_root;         ┘
13. COMMIT;
```

pgmi executes this queue top to bottom. If any test raises an exception at step 6–11, PostgreSQL aborts the transaction and `COMMIT` at step 13 never runs. Your migrations from steps 2–3 are rolled back. **The database is unchanged.**

If all tests pass, the savepoints roll back the test data (so it doesn't persist), but the migrations remain, and `COMMIT` makes them permanent.

**Successful deployment implies all tests passed. Failed tests mean zero changes to your database.**

### How this differs from `pgmi test`

| | `pgmi test` | Gated deployment |
|---|-------------|-----------------|
| When to use | After deployment, for verification | During deployment, as a safety gate |
| Who controls the transaction | pgmi (always rolls back) | You (via `deploy.sql`) |
| What happens on success | Everything rolls back | Migrations commit, test data rolls back |
| What happens on failure | Everything rolls back | Everything rolls back (including migrations) |

---

## Running tests

```bash
# Deploy first (tests need the schema to exist)
pgmi deploy . --overwrite --force

# Run all tests
pgmi test .

# Run with verbose output (shows PostgreSQL DEBUG messages)
pgmi test . --verbose

# Filter by path pattern (POSIX regex)
pgmi test . --filter "/orders/"

# List tests without executing
pgmi test . --list
```

The `pgmi test` command:
- Does **not** deploy anything — run `pgmi deploy` first
- Runs **only** files from `__test__/` or `__tests__/` directories
- **Always** rolls back — zero side effects on your database
- Stops at the **first failure** — no partial results to interpret

---

## Troubleshooting

### "relation does not exist"

Your test references a table that hasn't been deployed yet. Run `pgmi deploy . --overwrite --force` before running tests.

### Test passes alone but fails with others

This usually means one test depends on state from another test (a row it inserted, a sequence value). Fix: move the shared state into `_setup.sql` so every test gets it from the fixture.

### Fixture is getting too large

Split into subdirectories. Each subdirectory gets its own `_setup.sql` that builds on the parent fixture. See [Hierarchical fixtures](#hierarchical-fixtures).

### Tests are slow

Each test creates and rolls back a savepoint. This is fast for PostgreSQL. If tests are slow, the bottleneck is likely your SQL logic, not the test framework. Check for missing indexes or expensive queries in your fixtures.
