# Test-gated deployment

The canonical pgmi demonstration: schema changes, database tests, and the
commit decision in **one PostgreSQL transaction**. A failing test means the deployment
never happened.

![Test-gated deployment: apply files, test the changed database, commit only if tests pass](../../docs/diagrams/d00-test-gated-deploy.drawio.svg)

CI runs this exact example on every push — both the success path and the
failure path (`.github/workflows/ci.yml`, job `example-test-gated`).

## Run it

Any reachable PostgreSQL works; a disposable Docker container is the fastest:

```bash
docker run -d --name pgmi-demo -e POSTGRES_PASSWORD=postgres -p 5434:5432 postgres:18-alpine
docker exec pgmi-demo timeout 60 sh -c 'until pg_isready -h 127.0.0.1 -q; do sleep 1; done'
```

If either command prints an error (say, the port is already in use), stop: run
`docker rm pgmi-demo` and pick another port, in both `-p` and the connection string.

```bash
export PGMI_CONNECTION_STRING="postgresql://postgres:postgres@127.0.0.1:5434/postgres"
# PowerShell: $env:PGMI_CONNECTION_STRING = "postgresql://postgres:postgres@127.0.0.1:5434/postgres"

pgmi deploy . -d pgmi_example --param env=dev
```

`deploy.sql` runs `physical/` (tables, once per database) and `semantic/`
(views and functions, every deploy) in path order, then `CALL pgmi_test()` runs
every test in the `__test__/` directories inside the same transaction (each test isolated in its own
savepoint, so its transactional test data is rolled back). Everything passes →
the transaction commits. Exit code `0`. Sequence advances and external effects
are not rolled back.

## Break it

`break-it/test_audit_log.sql` expects the `audit_log` table to contain a
`deploy` event. Nothing inserts one, so the test fails. Deploy to a fresh
database to see the full rollback:

```bash
cp break-it/test_audit_log.sql __test__/
pgmi deploy . -d pgmi_example_broken --param env=dev
echo $?     # 13 — SQL execution failed
```

The failing test aborts the whole transaction. Verify nothing changed:

```bash
docker exec pgmi-demo psql -U postgres -d pgmi_example_broken -tAc \
  "SELECT to_regclass('public.audit_log') IS NULL AS rolled_back"
# t — the table from physical/003_audit_log.sql does not exist
```

The migration ran, the test failed, and PostgreSQL rolled both back together.
That is the test gate: only a verified deployment commits.

Restore the passing state with `rm __test__/test_audit_log.sql`, and clean up
with `docker rm -f pgmi-demo`.

## Files

| Path | Role |
|------|------|
| `deploy.sql` | The deployment program: runs the files, runs the tests, commits |
| `physical/` | Tables, keys and indexes in run-once files |
| `semantic/` | Views and writer functions, re-run on every deploy |
| `__test__/` directories | Tests run by `CALL pgmi_test()` inside the deploy transaction |
| `break-it/` | A failing test to copy in when you want to see the rollback |

See the [Testing Guide](../../docs/TESTING.md) for fixtures and hierarchical
test directories, and the [deploy.sql Guide](../../docs/DEPLOY-GUIDE.md) for
more patterns.
