# Your Application Tests

Tests of the project as a whole go here. A test sits next to what it tests:
an API test goes in `api/__test__/`, where `_setup.sql` signs up Alice as the
owner of Acme Corp (`test.alice_subject`, `test.acme_org_id`); a kernel test
next to its `core/` file. Tests run after deployment in savepoints that roll
back their transactional changes. Sequence advances and external effects are
not rolled back.

## Example Test Structure

```
api/__test__/
└── test_my_handlers.sql
core/__test__/
└── test_business_logic.sql
__test__/
└── test_whole_project.sql
```

## Writing Tests

Tests use pure PostgreSQL - no framework required. A test file takes no
`<pgmi-meta>` block:

```sql
DO $$
BEGIN
    -- Test your handler
    IF (api.rest_invoke('GET', '/my-endpoint', ''::extensions.hstore, NULL::bytea)).status_code IS DISTINCT FROM 200 THEN
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
