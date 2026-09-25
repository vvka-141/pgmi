# Apply-once tracking

Migrations that run once each, recorded in a ledger you own, with a drift
check that fails the deploy when an applied migration is edited. It is the
Flyway and Sqitch model, written as about forty lines of `deploy.sql` that you
can read and change.

Three behaviors, all asserted in CI (`example-apply-once` job):

1. **Apply once.** `001_customer.sql` is a plain `CREATE TABLE` and
   `002_seed_customers.sql` a plain `INSERT`. Neither can run twice, and the
   second deploy runs neither: `app.migration_log` records what ran.
2. **Drift fails the deploy.** Edit an applied migration so it does something
   different and the next deploy exits 13, naming the file, before it runs
   anything.
3. **Reformatting is not drift.** Recase keywords, move whitespace or add
   comments to an applied migration and the deploy still succeeds.

## Which checksum, and why

`pgmi_source_view` offers two:

- `checksum` is the SHA-256 of the raw bytes. Any edit changes it.
- `pgmi_checksum` is taken after stripping SQL comments, collapsing
  whitespace and folding the case of keywords and unquoted identifiers. It
  leaves string literals and quoted identifiers alone, so `'Ada'` and `'ada'`
  still differ.

This example tracks `pgmi_checksum`, so behavior 3 holds. Track `checksum`
instead if any reformatting should count as a change. Do not use
`pgmi_checksum` for data files such as CSV or JSON: its SQL-aware rules can
hide a real change to the data.

## Run it

Needs a PostgreSQL server (any recent version; a throwaway container works):

```bash
docker run -d --name pgmi-example -e POSTGRES_PASSWORD=postgres -p 5440:5432 postgres:17
docker exec pgmi-example timeout 60 sh -c 'until pg_isready -h 127.0.0.1 -q; do sleep 1; done'
```

```bash
export PGMI_CONNECTION_STRING="postgresql://postgres:postgres@127.0.0.1:5440/postgres"
# PowerShell: $env:PGMI_CONNECTION_STRING = "postgresql://postgres:postgres@127.0.0.1:5440/postgres"

cd project
pgmi deploy . -d apply_once_demo --force
pgmi deploy . -d apply_once_demo --force
```

The first deploy prints `apply-once: 2 migration(s) applied, 0 already
recorded`. The second prints `0 migration(s) applied, 2 already recorded`.

## Break it

From `project/`:

```bash
cp ../break-it/edited/001_customer.sql migrations/
pgmi deploy . -d apply_once_demo --force      # exit 13: applied migration(s) changed since they ran
cp ../break-it/reformatted/001_customer.sql migrations/
pgmi deploy . -d apply_once_demo --force      # exit 0: same statement, different formatting
git checkout -- migrations/001_customer.sql
```

The failed deploy rolls back whole, so the edited `CREATE TABLE`, which adds
an `email` column, never runs.

## What this is not

It does not order by version numbers, baseline an existing database, or
repair the ledger. Each of those is another query or `IF` in `deploy.sql`
when you need it. The drift policy above fails the deploy; warning or
ignoring is a one-line change in the same block.
