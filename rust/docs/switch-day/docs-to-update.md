# Switch day: doc lines to change

Collected by the docs review (PGMI-871, 2026-11-11). The public docs describe
the Go binary until switch day (PGMI-835), so none of this is applied before.
Line numbers are from main on 2026-11-11; re-check them on the day.

## Behaviour that changed on purpose (each a row in the 0.20 table)

- `PGCLIENTENCODING` is honoured: CLI.md:776-804 (env table), CICD.md:239
  (move out of "Not supported").
- `hostaddr` is honoured, no lookup: CONNECTIONS.md:170-171.
- Connection-string errors never quote the string: SECURITY.md:78-85 (drop
  the `net/url` wording).
- A client key readable by group or others is refused: CONNECTIONS.md:258-272
  ("the key file must be 0600").
- `--version` prints Go's two lines, with Rust's build: QUICKSTART.md:142-145
  (check the example output); session-api.md:78 (drop "`dev` for an untagged
  build").
- Known secrets are `***` in output: SECURITY.md (add a line).
- `pgmi init` writes no `sslmode`: CONNECTIONS.md (Azure section),
  QUICKSTART.md, `docs/LAKEBASE.md` (the staged `--databricks` section,
  `lakebase-databricks.md`, says `prefer` is refused: drop that sentence).

- `pgmi serve` keeps the operator's credentials for the servers it allows
  (PGMI-915): document `PGMI_SERVE_HOSTS` in CLI.md (the `serve` section and
  the env table) with the text staged in `switch-day/serve-allowed-hosts.md`.

## Go internals named in the docs (wording, not behaviour)

- CLI.md:806: "pgmi uses the jackc/pgx driver"; rewrite without pgx.
- CONFIGURATION.md:161-162: Go test names; cite the Rust tests
  (`conn/resolve_tests.rs`) or the cases.
- CONNECTIONS.md:125, 141-142, 467: the Cloud SQL Go connector; Rust has its
  own dialer (record 0009). Same behaviour: TLS by the dialer, inner
  `sslmode=disable`.
- CONNECTIONS.md:88, PRODUCTION.md:670-673: "a fresh token for each new
  connection; lifetime capped at the token's"; reword for the Rust binary.
- CONNECTIONS.md:170-178, CICD.md:199-201, 230: pgx and its three options;
  Rust uses the simple protocol and unnamed statements (record 0004), so
  PGlite needs none of them.
- CONNECTIONS.md:353-358, 456-471 and diagram d07: pool, `::int` probe,
  `StandardConnector`, `TokenBasedConnector`, `azidentity`,
  `rds/auth.BuildAuthToken`, `*pgx.Conn`; rewrite the section and the
  diagram.
- SECURITY.md:131: `set_config($1, $2, false)`; Rust binds one JSON value
  (still a bind parameter, so `log_parameter_max_length = 0` still hides it).
- PRODUCTION.md:37, 42: "set in the Go core" becomes "set in pgmi".
- session-api.md:13, 44, 508, 563; DEPLOY-GUIDE.md:15 and diagram d03: "Go
  code", "Go expands", "Go preprocessor" become "pgmi".
- TRADEOFFS.md:102, 104; core-vs-template.md:24, 34; WHY-PGMI.md:170;
  COMING-FROM.md:156; docs/design/api-versioning.md:40, 68, 124, 132-145;
  articles `deployment-is-a-postgresql-program.md:243`,
  `migration-numbers-distributed-counter.md:235`: "Go memory", "Go binary",
  "expanded by Go", "Go build".

## The Go library is dropped (owner, 2026-11-09)

- Delete docs/GO-LIBRARY.md and its links: README.md:124, 352;
  core-vs-template.md:17, 52-53; CONNECTIONS.md:344-345.
- Release notes: the library is gone; pin `go-legacy` (PGMI-835).

## Install

- `go install` and APT no longer apply: QUICKSTART.md:22, 149-165, 532;
  README.md:4 (Go badge), 293-296; website/static/llms.txt:6;
  CICD.md:454-456; docs/advanced/UPGRADING.md:37-39 (add "or the release
  archive for 0.20 and later").
- `.github/workflows/examples.yml` builds Go; point it at the Rust binary,
  so the six examples' "asserted in CI" covers what ships.

## Claims to re-date or re-test

- CONNECTIONS.md:40-45: the real-Azure hand test. Rust was run on real Azure
  on 2026-11-11 (PGMI-845); update the line with that run.
