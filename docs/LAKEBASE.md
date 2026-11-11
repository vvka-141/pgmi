---
title: "Databricks Lakebase"
description: "Deploy a pgmi project to Databricks Lakebase from your machine or from a serverless job in a Declarative Automation Bundle (Asset Bundle), including the advanced template and a test gate on a Lakebase branch."
weight: 132
---

# Databricks Lakebase

pgmi deploys to Databricks Lakebase like any PostgreSQL server: one direct
session to the database's endpoint. This page covers what differs: the
credential, running pgmi from a bundle job, the advanced template's provider
settings, and a test gate on a Lakebase branch.

> **What was checked, and when.** Lakebase projects (autoscaling, the
> `databricks postgres` API) on Azure Databricks, westeurope, PostgreSQL 18.6,
> Databricks CLI v1.24.0, between 2026-11-06 and 2026-11-09:
>
> - a serverless bundle job deploying the basic and the advanced template with
>   pgmi v0.13.1, and the basic template with v0.14.0;
> - the advanced template with the provider settings, from a workstation, on a
>   v0.14.1 pre-release build;
> - the branch test gate, from a workstation, with v0.14.0.
>
> The job script below is condensed from the tested one. Lakebase changes
> quickly: check the
> [Databricks Postgres documentation](https://learn.microsoft.com/azure/databricks/oltp/)
> for your region before relying on a detail.

## Connect

| Setting | Value |
|---|---|
| Host | the endpoint's direct host (`databricks postgres get-endpoint <endpoint>`, `status.hosts.host`) |
| Port, SSL | `5432`, `sslmode=require` |
| User | a Databricks identity with a Postgres role: the user name, or a service principal's application ID |
| Password | a database credential for that identity, valid for one hour |
| Maintenance database | `databricks_postgres` |

```bash
set -euo pipefail
token=$(databricks postgres generate-database-credential \
  projects/<project>/branches/production/endpoints/primary -o json | jq -er .token)
export PGPASSWORD=$token
pgmi deploy . --connection "host=<endpoint host> port=5432 dbname=databricks_postgres user=<you> sslmode=require" \
  -d myapp --param env=dev
```

Do not run this under `set -x`: the trace prints the token. Get the credential
right before the deploy. Lakebase checks its expiry only at login, so a deploy
that is already connected may run past the hour.

A plain Microsoft Entra access token is refused as the password, so pgmi's
`--azure` does not work against Lakebase; get the credential first, as above.

Only the identity that created the project has a Postgres role from the start.
Another user or service principal needs one before it can connect: after
`CREATE EXTENSION IF NOT EXISTS databricks_auth`, run
`SELECT databricks_create_role('<name>', 'USER')` (`'SERVICE_PRINCIPAL'` with
its application ID), then grant it what it needs.

The creator can `CREATE DATABASE`, so `-d` may name a new database. It also has
`BYPASSRLS` and belongs to `databricks_superuser`: row-level security does not
filter what it reads, even on a forced table. Never let an application do a
user's work as the deploy identity.

## Deploy from a bundle job

A serverless job task can download pgmi, get a credential for the identity it
runs as, and deploy. No credential configuration is needed inside the job: the
Databricks SDK authenticates as the job's identity, which is the identity that
deployed the bundle unless the bundle sets `run_as`. When that identity created
the Lakebase project, it has the rights the deploy needs.

```yaml
# resources/db_deploy.yml
resources:
  jobs:
    db_deploy:
      name: myapp-db-deploy
      max_concurrent_runs: 1
      tasks:
        - task_key: pgmi_deploy
          environment_key: default
          spark_python_task:
            python_file: ../jobs/deploy_db.py
            parameters:
              - --db-dir=${workspace.file_path}/db
              - --endpoint=projects/myapp/branches/production/endpoints/primary
              - --database=myapp
              - --param=env=prod
      environments:
        - environment_key: default
          spec:
            environment_version: "5"
            dependencies:
              - databricks-sdk>=0.81.0
```

```python
# jobs/deploy_db.py
import argparse, hashlib, os, platform, subprocess, tarfile, tempfile, urllib.parse, urllib.request
from databricks.sdk import WorkspaceClient

VERSION = "0.14.1"
BASE = f"https://github.com/vvka-141/pgmi/releases/download/v{VERSION}"
ASSET = {"aarch64": f"pgmi_{VERSION}_linux_arm64.tar.gz",
         "x86_64": f"pgmi_{VERSION}_linux_amd64.tar.gz"}[platform.machine()]
# From the release's checksums.txt, after `gh attestation verify <archive> -R vvka-141/pgmi`.
SHA256 = {"pgmi_0.14.1_linux_arm64.tar.gz": "<sha256>",
          "pgmi_0.14.1_linux_amd64.tar.gz": "<sha256>"}

p = argparse.ArgumentParser()
for a in ("--db-dir", "--endpoint", "--database"):
    p.add_argument(a, required=True)
p.add_argument("--param", action="append", default=[])
args = p.parse_args()

with tempfile.TemporaryDirectory() as tmp:
    archive = urllib.request.urlopen(f"{BASE}/{ASSET}", timeout=60).read()
    if hashlib.sha256(archive).hexdigest() != SHA256[ASSET]:
        raise RuntimeError(f"{ASSET} does not match its pinned SHA-256")
    path = os.path.join(tmp, ASSET)
    with open(path, "wb") as f:
        f.write(archive)
    with tarfile.open(path) as tar:
        tar.extractall(tmp, filter="data")

    w = WorkspaceClient()
    host = w.postgres.get_endpoint(name=args.endpoint).status.hosts.host
    user = w.current_user.me().user_name
    token = w.postgres.generate_database_credential(endpoint=args.endpoint).token
    if not (host and user and token):
        raise RuntimeError("no host, user or database credential for the endpoint")
    q = urllib.parse.quote
    conn = f"postgresql://{q(user, safe='')}:{q(token, safe='')}@{host}:5432/databricks_postgres?sslmode=require"
    cmd = [os.path.join(tmp, "pgmi"), "deploy", args.db_dir, "-d", args.database, "--force"]
    for param in args.param:
        cmd += ["--param", param]
    rc = subprocess.run(cmd, env={**os.environ, "PGMI_CONNECTION_STRING": conn}).returncode
if rc != 0:
    raise RuntimeError(f"pgmi deploy failed with exit code {rc}")
```

The SHA-256 values are pinned in the script on purpose. The archive and
`checksums.txt` come from the same release, so comparing one with the other
detects a corrupted download, not a replaced one.

What was observed on the serverless runtime:

- **Both architectures.** A serverless task ran on `aarch64` or `x86_64`, and
  an automatic retry of the same run switched between them. Pick the release
  asset from `platform.machine()`.
- **A retry runs the deploy again.** That is safe when the deploy converges, as
  the basic and advanced templates do. Otherwise turn off serverless
  auto-optimization for the job, which is what retries it.
- **No `sys.exit()`.** In a serverless `spark_python_task` (environment
  version 4), even `sys.exit(0)` marked the task failed. End normally on
  success and raise on failure.
- **Database driver.** `pg8000` (pure Python) worked when the job also queried
  PostgreSQL itself; `psycopg[binary]` aborted at import on `aarch64`
  (environment version 4, cause not found).
- **Outbound access to GitHub** for the download worked in the tested account.
- **The token only in the environment.** pgmi reads it from
  `PGMI_CONNECTION_STRING`; never pass it as a command-line argument or print
  it.

### The advanced template in a job

The advanced template needs more than `env`: the provider settings below, and
with `env=test` or `env=prod` an admin password. Pass the settings as
`--param` entries. Read the password from a Databricks secret and add it to
the environment of the `pgmi` call as `PGMI_PARAM_DATABASE_ADMIN_PASSWORD`,
never as a `--param`. This combination has not been run as a job yet.

## The advanced template on Lakebase

Observed, though not in the Databricks documentation: every database carries
event triggers that grant Lakebase's own roles (`databricks_reader_<id>`,
`databricks_writer_<id>`, `databricks_superuser`, `databricks_gateway`) on every
new schema, table, view and sequence, and a schema `__db_system`. The advanced
template's grant guards refuse that until you name them:

```text
provider_role_pattern=databricks\_%
provider_schemas=["__db_system"]
```

With both set, the deploy revokes the provider roles' `CREATE` on the
template's schemas and their `TRUNCATE`, `REFERENCES`, `TRIGGER` and `MAINTAIN`
on its tables, and the unmodified template deploys and redeploys with all its
tests. [Grants a provider makes on its own](PRODUCTION.md#grants-a-provider-makes-on-its-own)
explains what the guards still check.

What this means for your own tables:

- **Lakebase's reader can `SELECT` every new table.** The Databricks
  documentation shows the reader and writer roles with `Bypass RLS`; on the
  tested projects they did not have it. Check yours:
  `SELECT rolname, rolbypassrls FROM pg_roles WHERE rolname LIKE 'databricks\_%';`.
  Where they do not bypass it, a table forced to row-level security with
  policies keeps them out; a role that bypasses it is never held back by a
  policy. `databricks_superuser`, the admin group, does bypass it. A table without row-level security is readable by the reader from
  the moment it exists. The template's `test_forced_rls.sql` refuses one that
  holds an organization's rows.
- **The admin login cannot sign in by password.** New Lakebase projects refuse
  password logins unless the project
  [allows them](https://learn.microsoft.com/azure/databricks/oltp/projects/manage-roles#block-connections),
  which was not tested here. Sign in with your Databricks identity.
- **Pass a strong admin password even with `env=dev`.** Lakebase checks
  passwords set in SQL for strength, and the template's `env=dev` default,
  `postgres`, was not tried there. The tested runs passed 48 random hex
  characters.

## A test gate on a branch

A Lakebase branch is a copy of another branch's schema, data and roles, taken
when the branch is created. Deploy to a branch first, and to `production` only
when that deploy passes:

```bash
set -euo pipefail
P=projects/myapp
B=gate-$(date +%s)
databricks postgres create-branch $P $B --ttl 3600s
trap 'databricks postgres delete-branch $P/branches/$B' EXIT

deploy() {   # deploy <branch>: a credential for that branch's endpoint, then pgmi
  local ep=$P/branches/$1/endpoints/primary host token
  host=$(databricks postgres get-endpoint $ep -o json | jq -er .status.hosts.host)
  token=$(databricks postgres generate-database-credential $ep -o json | jq -er .token)
  PGPASSWORD=$token pgmi deploy . -d myapp --param env=prod \
    --connection "host=$host port=5432 dbname=databricks_postgres user=<you> sslmode=require"
}

deploy $B            # the tests run on the copy; a failure stops the script here
deploy production
```

`create-branch` with only `--ttl` branched from `production` in the tested
runs; the `--ttl` deletes a branch the `trap` misses. Observed: the branch was
connectable 25 seconds after `create-branch`, had every role of `production`,
and a failing test left `production` unchanged. The cycle took about two
minutes.

A pass on the branch proves the deploy against that snapshot. `production` can
change before the second deploy, so keep the tests in the `production` deploy
too (the templates run them on every deploy), and let one pipeline at a time
deploy to `production`.
