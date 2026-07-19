# Execution-order policy

A phased catalog load demonstrating that in pgmi the deployment plan is a
query result (`pgmi_plan_view`) the project can enforce its own ordering
policy against. Companion to the article "Your Migration Numbers Are a
Distributed Counter Without Coordination."

Three behaviors, all asserted in CI (`example-execution-order` job):

1. **Multi-position execution** — `checks/smoke.sql` declares two sort keys
   (`150/000`, `400/000`) and runs at plan positions #2 and #6: once after the
   schema (`smoke: ok (0 products, 0 prices)`), once after the load and the
   validated foreign key (`smoke: ok (5 products, 5 prices)`).
2. **Duplicate-position policy** — `deploy.sql` rejects two files claiming the
   same sort key. pgmi itself orders ties deterministically by path; whether
   that is acceptable is the project's decision, written as SQL.
3. **Plan-manifest assertion** — `deploy.sql` diffs `pgmi_plan_view` against a
   reviewed manifest and aborts on any drift, before executing anything.

## Run it

Needs a PostgreSQL server (any recent version; a throwaway container works):

```bash
docker run -d --name pgmi-example -e POSTGRES_PASSWORD=postgres -p 5440:5432 postgres:16
export PGMI_CONNECTION_STRING="postgresql://postgres:postgres@127.0.0.1:5440/postgres"

cd project
pgmi deploy . -d catalog_demo --force
```

Expected: the printed plan shows six rows for five files, `smoke.sql` at #2
and #6, exit code 0.

## Break it

**Duplicate position** — a second branch picked `200/010`, already claimed by
`load/010_products.sql`:

```bash
cp ../break-it/012_categories.sql load/
pgmi deploy . -d catalog_demo --force
# ✗ ERROR: duplicate plan positions:
#   200/010: ./load/010_products.sql, ./load/012_categories.sql
# exit code 13, no planned source file executed
rm load/012_categories.sql
```

**Manifest drift** — a new file lands in a reasonable position, but nobody
updated the manifest in `deploy.sql`:

```bash
cp ../break-it/015_discounts.sql load/
pgmi deploy . -d catalog_demo --force
# ✗ ERROR: plan does not match the reviewed manifest:
#   #4: manifest has (200/020, ./load/020_prices.sql), plan has (200/015, ./load/015_discounts.sql)
#   ...
# exit code 13, no planned source file executed
rm load/015_discounts.sql
```

## Layout

```
project/
├── deploy.sql                 print plan → collision gate → manifest assert → execute
├── schema/010_catalog.sql     sortKey 100/010  (tables; no index/FK yet — bulk-load shape)
├── checks/smoke.sql           sortKeys 150/000 and 400/000  (one file, two plan rows)
├── load/010_products.sql      sortKey 200/010
├── load/020_prices.sql        sortKey 200/020
└── post/010_indexes.sql       sortKey 300/010  (index + FK NOT VALID → VALIDATE)
break-it/
├── 012_categories.sql         duplicate sort key 200/010 → collision gate fires
└── 015_discounts.sql          unlisted sort key 200/015 → manifest assertion fires
```

The README sits outside `project/` on purpose: pgmi loads every project file
into the session, so a README inside the deploy root would itself become a
plan row — and fail the manifest assertion.
