# Rust re-implementation: decision records

Design decisions for the Rust pgmi binary (PGMI-829), written 2026-11-10 from
`docs/design/behaviour-contract.md`, the black-box suite and the spike
verdicts (`spike-a-connection.md`, `spike-b-cloudsql.md`,
`spike-d-release.md`). Every crate version was checked on crates.io or
docs.rs on that date. All records are **proposed** until the owner accepts
them.

## Records

| Record | Decision |
|---|---|
| [0001](0001-crate-layout.md) | Virtual workspace at the repo root: binary `rust/pgmi`, assets `shared/` (`pgmi-shared`); edition 2024, rust-version 1.94.1, toolchain 1.99.1 |
| [0002](0002-errors-and-exit-codes.md) | One `pgmi::Error` carrying its kind from creation; `catch_unwind` + panic hook make a panic exit 3 (or `isError` in serve); SIGPIPE restored |
| [0003](0003-async-runtime.md) | tokio multi-thread runtime, started after the environment is settled; one cancellation token per deploy |
| [0004](0004-postgres-connection-layer.md) | tokio-postgres `connect_raw` with pgmi's own libpq-style resolution; the five decided libpq behaviours; simple protocol and unnamed statements only |
| [0005](0005-tokio-postgres-unpatched.md) | tokio-postgres from crates.io unpatched; a 195-line layer on pgmi's own connection reads the transaction status and enforces `require_auth` (spike: six of six cases pass); the fork patch kept as fallback only |
| [0006](0006-tls.md) | rustls on graviola (owner, 2026-11-11; CPU features checked first, record 0021), one verifier for all sslmodes, rustls-native-certs for `system`, own legacy-key decryption and own tokio-postgres TLS adapter |
| [0007](0007-azure-credentials.md) | azure_identity without default features, pgmi's own chain; certificate service principal via a self-signed client assertion (no OpenSSL); every Entra route listed with its real-Azure coverage |
| [0008](0008-aws-credentials.md) | aws-config credentials on pgmi's own reqwest-based HTTP client; the RDS token signed with aws-sigv4 directly, not aws-sdk-rds |
| [0009](0009-google-cloud-sql.md) | Spike B's own dialer; google-cloud-auth without default features; Admin API URL overridable for fakes |
| [0010](0010-embedded-assets-and-packaging.md) | Own `build.rs` table in `pgmi-shared`; `include` lists; git, package list and embedded list must be equal |
| [0011](0011-cli-parsing.md) | clap derive; Cobra's completion scripts answered by `__complete`; long-only `--help`, Go-style booleans and durations, exit codes in help |
| [0012](0012-terminal-ui.md) | ratatui + crossterm state machines for the two wizards, plain prompts elsewhere; detection rules for CI, colour, banner, pager |
| [0013](0013-yaml.md) | serde-saphyr to read (strict, source text kept), yaml-edit to edit keeping comments |
| [0014](0014-pgmiignore-and-sql-lexer.md) | Own clean-room gitignore matcher with POSIX classes; one hand-written SQL lexer for split, macro, checksum, metadata |
| [0015](0015-checksums.md) | sha2; normaliser on the shared lexer, whitespace set spelled out; golden and cross-binary cases |
| [0016](0016-environment-and-dotenv.md) | Own `.env`/params parser with a strict mode; environment snapshot map; `.env` keys exported before any thread exists |
| [0017](0017-mcp-server.md) | Hand-written JSON-RPC server, as in Go; rmcp plus pgmi's own transport is the fallback only (rmcp's transport drops unparsable lines silently) |
| [0018](0018-release-and-packaging.md) | GoReleaser OSS run on a macOS runner (prebuilt import is Pro only); wheels and npm packages assembled from the release binaries |
| [0019](0019-output.md) | Own stderr Reporter, no tracing subscriber; one serde struct for `--json` and the serve deploy result; jiff timestamps |
| [0020](0020-metadata-xml.md) | roxmltree for `<pgmi-meta>`, uuid for ids; scaffold stops at `.git` |
| [0021](0021-pure-rust.md) | Pure Rust (PGMI-855): features that keep C out of every cloud crate, a CI gate banning C-compiling crates and checking every target with no C compiler; graviola and the platform linkers accepted by the owner (2026-11-11) |
| [0022](0022-library-layout-and-seams.md) | One package with a lib target and a thin bin; `pub(crate)` by default; seams only where a test double exists (`Vfs`, `Approver`, Reporter sink, serve over any transport); `deploy::Request`, `DeployRecord`, typed settings and `Secret`; an ordered refactor plan that keeps the black-box suite green after each step |
| [0023](0023-databricks-lakebase.md) | Databricks Lakebase sign-in hand-written (PGMI-849): `POST /api/2.0/postgres/credentials`, PAT, OAuth M2M, the Databricks CLI and Azure, a credential refreshed five minutes before it expires; `impl: rust` black-box cases against a fake workspace |
| [0024](0024-templates-on-every-host.md) | Templates that deploy and pass their tests on local PostgreSQL and every listed cloud host with only the README's edits (PGMI-874) |
| [0025](0025-whole-deploy-retry.md) | Accepted (PGMI-882): a transient failure reruns the whole deploy from the start, like a redeploy, inside the `--timeout` time box; the retried and never-retried failures, backoff, reporting, open choices for the owner |
| [go-review](go-review.md) | PGMI-830: Go behaviour missing from the contract, the cases added for it, and six owner questions |

## Contract coverage

Every `###` section of the behaviour contract and the records that serve it.

| Contract section | Records |
|---|---|
| Exit code table | 0002, 0011, 0001 |
| info | 0011, 0014, 0019 |
| init | 0010, 0011, 0012 |
| Template deployments | 0010, 0015, 0004, 0014 |
| metadata | 0020, 0014, 0011 |
| templates, version, ai, completion, help | 0011, 0010, 0012 (pager), 0018 (version string) |
| Verbose output | 0019, 0014, 0011 |
| serve | 0017, 0002, 0003, 0019 |
| config | 0012, 0013, 0004 (connection test) |
| Project discovery and loading | 0014 |
| Session API | 0010, 0004, 0015, 0020 |
| Parameters | 0016, 0013, 0004 (session settings) |
| Connection sources | 0004, 0016, 0013 |
| Connection resolution | 0004, 0005 |
| TLS | 0006, 0005, 0004 |
| Target database, overwrite and approval | 0012, 0004, 0010 (manager SQL) |
| Concurrency | 0004, 0005, 0017 |
| Execution contract | 0014, 0004, 0005 |
| Test macro | 0014, 0010 |
| Notices and the summary line | 0019, 0004, 0012 |
| The --json record | 0019, 0002 |
| Error attribution | 0019, 0014, 0002 |
| Timeout, cancellation, interruption | 0003, 0004, 0002, 0011 |
| Checksums | 0015, 0014 |
| File formats | 0016, 0013 |
| Terminal behaviour | 0012, 0016 |
| Cloud sign-in | 0007, 0008, 0009, 0006 |
| Databricks Lakebase sign-in | 0023, 0007, 0006 |
| Google sign-in failure is exit 11 | 0009, 0002 |
| Every address's error is reported | 0004 |
| POSIX character classes in .pgmiignore | 0014 |
| metadata scaffold stops at the repository root | 0020 |
| Azure service principal with a certificate | 0007 |
| Nothing that cannot be reached is carried over | 0007 (no username/password Entra login), 0009 (no metrics), 0020 (no XSD) |
| sslmode=allow retries with TLS | 0004, 0006 |
| Unknown connection-string keys are refused before connecting | 0004 |
| PGPORT may list several ports | 0004 |
| No password available | 0004 |
| Encrypted legacy client keys and require_auth are kept | 0006, 0005 |

## Settled on review (2026-11-10)

- "No password available": the contract was wrong; it now follows libpq
  (fail only when the server asks). (0004)
- Azure (owner review): Entra sign-in is must-ship. Certificate login for a
  service principal is added (Go never reached it); username/password Entra
  login is dropped. Record 0007 lists every route and its real-Azure run.
  Service Fabric and Azure ML managed identity are written for parity, not
  tested against the platform. (0007)
- The Cloud SQL dialer reads gcloud's
  `CLOUDSDK_API_ENDPOINT_OVERRIDES_SQLADMIN` as its test seam. (0009)
- Release (owner review): the whole release job runs on a macOS runner; no
  GoReleaser Pro. If that fails, report to the owner; the likely fallback is
  dropping the affected macOS build. npm platform packages are
  unscoped (`pgmi-cli-<os>-<arch>`), reserved in PGMI-850. (0018)
- Encrypted PKCS#8 client keys are added: libpq reads them, and a key that
  works with psql should work with pgmi. (0006)
- Driver (owner review): tokio-postgres unpatched plus pgmi's own layer, no
  fork and no upstream PR; the layer is permanent. The spike passed six of
  six. Done only with a `pgmi serve` case, a fuzz target and TLS start from
  sslmode; the fuzz target runs in the CI job `fuzz` (PGMI-861). The fork
  patches stay in `patches/` as reference only. (0005)
- MCP server (owner review): hand-written, like Go. Done only when it
  matches Go's behaviour under black-box cases (malformed input included)
  and the input parser has a fuzz target, run in the CI job `fuzz`
  (PGMI-861). rmcp plus pgmi's own transport is the fallback only. (0017)

## Open questions

Answered in PGMI-830 (review against Go). Findings beyond these are in
[go-review.md](go-review.md).

1. Symbolic links are never followed (exit 10 unless ignored; `deploy.sql`
   may be one); a NUL byte or invalid UTF-8 is exit 10; comments may stand
   between the macro's tokens, ASCII white space only. (0014;
   `dialect/project-walk`, `dialect/pgmi-test-macro-comments`,
   `dialect/pgmi-test-macro-whitespace`)
2. `.env` expands `$NAME`/`${NAME}` from earlier lines only; it feeds the
   cloud SDKs. (0016; `formats/dotenv-expansion`, `cloud/dotenv-feeds-sdk`)
3. Pager split on white space, no shell; default `less -R`; a pager that
   cannot start prints directly. (0012; `terminal/pager-command`)
4. Bare `pgmi`: help on stdout, exit 0; banner on stderr on a terminal.
   (0011; `commands/bare`)
5. Summary duration: seconds with two decimals. (0019;
   `notices/summary-line`)
6. Four id spellings, Go's lenient XML (text after the root ignored, which
   roxmltree does not do by itself), fallback id `md5("./<path>")`. (0020;
   `commands/metadata-forms`, `commands/metadata-refusals`)
7. Golden vectors with Unicode white space at the ends. (0015;
   `checksums/golden`)
8. Go does not embed the new `shared/` files. (0001; no case, checked on
   the embedded file list)

Settled on the first build (PGMI-832):

9. ~~google-cloud-auth signing service-account keys without aws-lc-rs.~~
   Answered from its source: it uses the installed provider. (0009)
10. serde-saphyr handing plain scalars to the visitor as written. (0013)
