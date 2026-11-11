# 0024: Templates that work on every host

Status: accepted, 2026-11-11. Ticket: PGMI-874 (owner decision 2026-11-11:
no CLI change). Builds on PGMI-873 (basic privilege test, no sslmode in
`pgmi init`). Research ledger: `.private/research/pgmi-874-host-assumptions.md`.

## Goal

A project from `pgmi init` (basic and advanced) deploys and passes its own
tests on local PostgreSQL, Azure Database for PostgreSQL, Amazon RDS and
Aurora, Google Cloud SQL and Databricks Lakebase, with only the edits its
README tells a user to make. The templates are shared files, so both
binaries get this; it ships with 0.20.

## Rules, in order of preference

1. **Host-agnostic SQL.** Correct on any host without knowing which.
2. **Detection in SQL** where behaviour must differ. It only fills in a
   default or makes an error clearer. It never relaxes a guard on an
   unknown host. An explicit `--param` always wins.
3. **The cloud test journey** does only what the README tells a user:
   `pgmi init`, the connection settings in `pgmi.yaml`, credentials, and
   `--param` values. It never patches template SQL or tests. A target that
   needs more is a template bug and gets a ticket.

## Decisions

### Layer 1

- **Basic privilege test** checks only the deploying role's functions
  (PGMI-873). A provider's functions in `public` no longer fail it.
- **`pgmi init` writes no sslmode** (PGMI-873).
- **The extensions stay.** `uuid-ossp` serves only `uuid_nil()`, but a
  run-once file (`lib/common/cast-operators.sql`) calls it, and editing a
  run-once file makes every existing database refuse its next deploy. One
  fewer entry in Azure's allow-list is not worth that. The five
  (`uuid-ossp`, `pgcrypto`, `pg_trgm`, `hstore`, `btree_gist`) are on every
  listed host's supported list; Azure needs them allow-listed, which the
  deploy now checks (below) and the README says.
- **Schemas are not judged by owner.** Treating every schema the template's
  roles do not own as foreign would make the schema guard pass any schema
  another role created, on every host. The guard stays strict; Lakebase's
  own schema is handled by detection.

### Layer 2: detection in the advanced deploy.sql

| Host | Signals (all must hold) | Effect |
|---|---|---|
| Lakebase | role `databricks_superuser` and schema `__db_system` | `provider_role_pattern` defaults to `databricks\_%`, `provider_schemas` to `["__db_system"]` |
| Azure | setting `azure.extensions` exists | before anything changes, the deploy names the required extensions missing from it and the `az postgres flexible-server parameter set` command that adds them |
| any | the login lacks `CREATEROLE` or `CREATEDB` and is not a superuser | before anything changes, the deploy names what is missing |

Two signals for Lakebase, because its defaults relax guards for the
provider's roles. A role named `databricks_superuser` on another host needs
`CREATEROLE` to create, so a user who can fake the signal can already
change roles. Passing `provider_role_pattern` or `provider_schemas`
explicitly overrides the detected value. No new parameter: the two existing
ones are the override.

Not detected, because nothing needs it yet: RDS (`rds_superuser`) and Cloud
SQL (`cloudsqlsuperuser`). Their published extension lists hold all three
extensions, and no run has shown a failure. Add a row when a run does.

### Layer 3: the cloud journey

A private script (`.private/cloud/journey.sh`) runs, per template and host:
`pgmi init`, write the connection lines into `pgmi.yaml`, `pgmi deploy . -d
<new db> --param env=dev` with the host's sign-in flag, then the same deploy
again (convergence), and records the exit codes. It refuses to edit any
other file. Reachable hosts: local, Azure and Lakebase (the owner limits real
clouds to Azure; Lakebase is PGMI-849's). Resource IDs of each run are kept
in a private file for an exact teardown, then deleted.

### README notes per host

Each template README gets a short "Managed PostgreSQL" section: the sign-in
flag, the login's needed attributes (`CREATEDB`; advanced also
`CREATEROLE`), the Azure extension allow-list (advanced), connecting
directly rather than through a transaction pooler, and the Lakebase
maintenance database.

## Left open, decided by evidence from a real run

- Whether Lakebase refuses the advanced template's dev admin password
  `postgres`. If it does, the dev default becomes a generated value.
- Whether `public` is owned by a provider group on Azure, RDS or Cloud SQL so
  that the deploy's revoke on it only warns. If so, the revoke moves before
  the deploy switches to the owner role.
