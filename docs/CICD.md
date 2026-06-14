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
      PGMI_VERSION: v0.10.0         # pin to a specific release tag
      DB_NAME: myapp
    steps:
      - uses: actions/checkout@v4

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
- **`--compat 1`** pins the pgmi session API so a pgmi upgrade can't change your
  deploy's behavior. Bump it deliberately after testing the new default. See
  [CLI Reference](CLI.md#understanding-compat-api-versioning).
- **`--force`** skips the interactive confirmation (there is no TTY in CI). It does
  **not** drop your database — that is `--overwrite`, which belongs only on
  throwaway/test databases, never production.
- **`PGMI_CONNECTION_STRING`** carries host, user, and password from a single secret.
  Point it at a direct connection, not a transaction-mode pooler.

### Passing role passwords (advanced template)

The advanced template sets role passwords at deploy time. Provide them via a params
file generated from secrets — never as command-line `--param` (argv leaks to the
process list and CI logs; see the [Security Guide](SECURITY.md)):

```yaml
      - name: Deploy
        env:
          PGMI_CONNECTION_STRING: ${{ secrets.DATABASE_URL }}
          DATABASE_ADMIN_PASSWORD: ${{ secrets.DATABASE_ADMIN_PASSWORD }}
          DATABASE_CUSTOMER_PASSWORD: ${{ secrets.DATABASE_CUSTOMER_PASSWORD }}
        run: |
          umask 077
          cat > "$RUNNER_TEMP/secrets.env" <<EOF
          database_admin_password=$DATABASE_ADMIN_PASSWORD
          database_customer_password=$DATABASE_CUSTOMER_PASSWORD
          EOF
          pgmi deploy . -d "$DB_NAME" --compat 1 --force \
            --params-file "$RUNNER_TEMP/secrets.env"
          rm -f "$RUNNER_TEMP/secrets.env"
```

## Other CI systems

The three steps — install, connect via secret, `pgmi deploy … --compat 1` — apply
anywhere. Alternatives for the install step:

- **Install script, pinned:**
  `curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | PGMI_VERSION=v0.10.0 bash`
  (convenient; does not checksum-verify). The `PGMI_VERSION` prefix must sit on
  `bash`, not `curl`, or the script falls back to the latest release.
- **Debian/Ubuntu runners (APT, GPG-verified):**
  `curl -1sLf 'https://dl.cloudsmith.io/public/vvka-141/pgmi/setup.deb.sh' | sudo bash && sudo apt install -y pgmi`.
- **Go-based pipelines only:** `go install github.com/vvka-141/pgmi/cmd/pgmi@v0.10.0`
  — pin the tag (never `@latest`); note this requires the Go toolchain and compiles
  from source, so it is slower and less reproducible than a release binary.

For a GitLab CI secrets example, see the
[Security Guide](SECURITY.md#gitlab-ci-example).
