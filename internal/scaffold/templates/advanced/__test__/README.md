# Your Application Tests

Place your test files here. Tests are executed after deployment in a transaction
that automatically rolls back, ensuring no side effects.

## Example Test Structure

```
__test__/
├── api/
│   └── test_my_handlers.sql
└── domain/
    └── test_business_logic.sql
```

## Writing Tests

Tests use pure PostgreSQL - no framework required:

```sql
/*
<pgmi-meta id="YOUR-UUID-HERE" idempotent="true">
  <description>My API tests</description>
</pgmi-meta>
*/

DO $$
BEGIN
    -- Test your handler
    IF (api.rest_invoke('GET', '/my-endpoint', NULL, NULL::bytea)).status_code != 200 THEN
        RAISE EXCEPTION 'Expected 200 OK';
    END IF;

    RAISE NOTICE '✓ My endpoint test passed';
END $$;
```

## Running Tests

Tests run as part of deployment via the `pgmi_test()` macro in deploy.sql:

```bash
pgmi deploy . -d your_database
```

If all tests pass, the deployment commits. If any test fails, everything rolls back.
