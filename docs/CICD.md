---
title: "CI/CD"
description: "Deploy pgmi projects from CI systems with pinned binaries, direct connections, secrets, and compatibility versions."
weight: 130
---

# CI/CD

Deploy pgmi projects from any CI system. The pattern is always the same: install a
**pinned** pgmi binary, point it at a **direct** database connection via secrets, and
run `pgmi deploy` with a pinned API version.

## Requirements

- **A direct PostgreSQL connection** (or a session-mode pooler). Transaction-mode
  poolers — PgBouncer in `transaction` mode, AWS RDS Proxy, Azure's built-in
  PgBouncer — reassign connections between statements and destroy the session-scoped
  temp tables pgmi relies on. See
  [Connection Requirements](PRODUCTION.md#connection-requirements).
- **Secrets from your CI secret store**, never on the command line. See the
  [Security Guide](SECURITY.md).

pgmi's exit codes are the pipeline contract — every failure point is numbered, so CI branches on `$?` instead of parsing output:

![The deploy sequence: validate, connect, lock, prepare the session, preprocess, then your deploy.sql runs — with exit codes at each failure point](diagrams/d04-deploy-sequence.drawio.svg)

## GitHub Actions

```yaml
name: Deploy database

on:
  push:
    branches: [main]

jobs:
  deploy:
    runs-on: ubuntu-latest
    env:
      PGMI_VERSION: v0.13.0         # pin to a specific release tag
      DB_NAME: myapp
    steps:
      - uses: actions/checkout@v5

      - name: Install pgmi (pinned, checksum-verified)
        run: |
          file="pgmi_${PGMI_VERSION#v}_linux_amd64.tar.gz"
          base="https://github.com/vvka-141/pgmi/releases/download/${PGMI_VERSION}"
          curl -fsSLO "${base}/${file}"
          curl -fsSLO "${base}/checksums.txt"
          sha256sum --ignore-missing -c checksums.txt
          tar -xzf "${file}" pgmi
          sudo install pgmi /usr/local/bin/pgmi
          pgmi --version

      - name: Deploy
        env:
          PGMI_CONNECTION_STRING: ${{ secrets.DATABASE_URL }}   # direct connection
        run: pgmi deploy . -d "$DB_NAME" --compat 1 --force
```

Why these choices:

- **Pinned, checksum-verified binary** — no Go toolchain, reproducible runs, and the
  download is integrity-checked against the release `checksums.txt`. Prefer this over
  `go install …@latest`, which recompiles every run and lets a new release silently
  change your deploy.
- **`--compat 1`** pins the session API: view and function names and columns.
  It does not pin behavior such as plan ordering, which files load, or how
  deploy.sql is sent. The binary version pins that, so pin both. Bump `--compat`
  deliberately after testing the new default. See
  [CLI Reference](CLI.md#understanding---compat-api-versioning).
- **`--force`** skips the interactive confirmation (there is no TTY in CI). It does
  **not** drop your database — that is `--overwrite`, which belongs only on
  throwaway/test databases, never production.
- **`PGMI_CONNECTION_STRING`** carries host, user, and password from a single secret.
  Point it at a direct connection, not a transaction-mode pooler.

### Passing role passwords (advanced template)

> **Scope: advanced template only.** This is SQL that `pgmi init --template advanced` copied into your project, not behaviour of the pgmi binary.

The advanced template sets role passwords at deploy time. Provide them via a params
file generated from secrets — never as command-line `--param` (argv leaks to the
process list and CI logs; see the [Security Guide](SECURITY.md)):

```yaml
      - name: Deploy
        env:
          PGMI_CONNECTION_STRING: ${{ secrets.DATABASE_URL }}
          DATABASE_ADMIN_PASSWORD: ${{ secrets.DATABASE_ADMIN_PASSWORD }}
        run: |
          umask 077
          cat > "$RUNNER_TEMP/secrets.env" <<EOF
          env=prod
          database_admin_password=$DATABASE_ADMIN_PASSWORD
          EOF
          pgmi deploy . -d "$DB_NAME" --compat 1 --force \
            --params-file "$RUNNER_TEMP/secrets.env"
          rm -f "$RUNNER_TEMP/secrets.env"
```

## Pull-request gate without a database

`pgmi metadata validate` and `pgmi metadata plan` read the project and never
connect. Run them on every pull request to catch bad `<pgmi-meta>` blocks,
duplicate ids and an unexpected execution order before anything touches a
database:

```bash
pgmi metadata validate . --json
pgmi metadata plan . --json
```

Both exit 10 on invalid metadata or duplicate ids. See
[CLI Reference](CLI.md#pgmi-metadata-plan) for the JSON shapes.

## Reading the result

`pgmi deploy --json` prints a JSON envelope to stdout on success and on
failure: exit code, SQLSTATE, the failing file and line. Branch on the exit
code; read the envelope for the report. See
[CLI Reference](CLI.md#--json-envelope).

## Concurrent deploys and timeouts

pgmi takes a per-database lock before it runs anything. A second
`pgmi deploy` against the same database does not wait: it exits 15 at once.
Serialize deploys in the CI system, for example with a GitHub Actions
`concurrency:` group per database, so a second run queues instead of failing.
Treat exit 15 as "another deploy is running", not as a broken deploy.

`--timeout` (default 3m) covers the whole deploy, including the autocommit tail.
A `CREATE INDEX CONCURRENTLY` that outlives it is cancelled: pgmi exits 16 and
the index is left `INVALID`. Raise `--timeout` for large tables, and write the
build so a re-run reaps the leftover (see
[making a concurrent index re-runnable](DEPLOY-GUIDE.md#making-a-concurrent-index-re-runnable)).

## Other CI systems

The three steps — install, connect via secret, `pgmi deploy … --compat 1` — apply
anywhere. Alternatives for the install step:

- **Install script, pinned:**
  `curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | PGMI_VERSION=v0.13.0 bash`
  (verifies the download against `checksums.txt`). The `PGMI_VERSION` prefix must sit on
  `bash`, not `curl`, or the script falls back to the latest release.
- **Debian/Ubuntu runners (APT, GPG-verified):**
  `curl -1sLf 'https://dl.cloudsmith.io/public/vvka-141/pgmi/setup.deb.sh' | sudo bash && sudo apt install -y pgmi`.
- **Go-based pipelines only:** `go install github.com/vvka-141/pgmi/cmd/pgmi@v0.13.0`
  — pin the tag (never `@latest`); note this requires the Go toolchain and compiles
  from source, so it is slower and less reproducible than a release binary.

For a GitLab CI secrets example, see the
[Security Guide](SECURITY.md#gitlab-ci-example).
