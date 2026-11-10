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
| [0006](0006-tls.md) | rustls on a pure-Rust provider (graviola until the owner chooses, record 0021), one verifier for all sslmodes, rustls-native-certs for `system`, own legacy-key decryption and own tokio-postgres TLS adapter |
| [0007](0007-azure-credentials.md) | azure_identity without default features, pgmi's own chain; certificate service principal via a self-signed client assertion (no OpenSSL); every Entra route listed with its real-Azure coverage |
| [0008](0008-aws-credentials.md) | aws-config credentials on pgmi's own reqwest-based HTTP client; the RDS token signed with aws-sigv4 directly, not aws-sdk-rds |
| [0009](0009-google-cloud-sql.md) | Spike B's own dialer; google-cloud-auth without default features; Admin API URL overridable for fakes |
| [0010](0010-embedded-assets-and-packaging.md) | Own `build.rs` table in `pgmi-shared`; `include` lists; git, package list and embedded list must be equal |
| [0011](0011-cli-parsing.md) | clap derive + clap_complete; long-only `--help`, Go-style booleans and durations, exit codes in help |
| [0012](0012-terminal-ui.md) | ratatui + crossterm state machines for the two wizards, plain prompts elsewhere; detection rules for CI, colour, banner, pager |
| [0013](0013-yaml.md) | serde-saphyr to read (strict, source text kept), yaml-edit to edit keeping comments |
| [0014](0014-pgmiignore-and-sql-lexer.md) | Own clean-room gitignore matcher with POSIX classes; one hand-written SQL lexer for split, macro, checksum, metadata |
| [0015](0015-checksums.md) | sha2; normaliser on the shared lexer, whitespace set spelled out; golden and cross-binary cases |
| [0016](0016-environment-and-dotenv.md) | Own `.env`/params parser with a strict mode; environment snapshot map; `.env` keys exported before any thread exists |
| [0017](0017-mcp-server.md) | Hand-written JSON-RPC server, as in Go; rmcp plus pgmi's own transport is the fallback only (rmcp's transport drops unparsable lines silently) |
| [0018](0018-release-and-packaging.md) | GoReleaser OSS run on a macOS runner (prebuilt import is Pro only); wheels and npm packages assembled from the release binaries |
| [0019](0019-output.md) | Own stderr Reporter, no tracing subscriber; one serde struct for `--json` and the serve deploy result; jiff timestamps |
| [0020](0020-metadata-xml.md) | roxmltree for `<pgmi-meta>`, uuid for ids; scaffold stops at `.git` |
| [0021](0021-pure-rust.md) | Pure Rust (PGMI-855): features that keep C out of every cloud crate, a CI gate banning C-compiling crates and checking every target with no C compiler; provider and platform linkers wait for the owner |

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
  sslmode. The fork patches stay in `patches/` as reference only. (0005)
- MCP server (owner review): hand-written, like Go. Done only when it
  matches Go's behaviour under black-box cases (malformed input included)
  and the input parser has a fuzz target. rmcp plus pgmi's own transport is
  the fallback only. (0017)

## Open questions

Answered in PGMI-830 (review against Go), each with a black-box case:

1. Does the project walk follow symbolic links? How is a binary file
   detected, and with which exit code? Comments between the macro's tokens?
   (0014)
2. `$VAR` expansion in `.env` values; does `.env` feed the cloud SDK
   variables? (0016)
3. Pager: shell or split, and any fallback when no pager variable is set?
   (0012)
4. Bare `pgmi` with no arguments: output and exit code. (0011)
5. Summary-line duration format. (0019)
6. Accepted id spellings, XML strictness, and the algorithm of the fallback
   id of a file without metadata (byte-exact if the binary makes it). (0020)
7. A golden checksum vector with non-ASCII white space at the ends. (0015)
8. Whether Go's embed patterns in `shared/` would pick up the new
   `Cargo.toml`, `build.rs` and `src/` files. (0001)

Settled on the first build (PGMI-832):

9. ~~google-cloud-auth signing service-account keys without aws-lc-rs.~~
   Answered from its source: it uses the installed provider. (0009)
10. serde-saphyr handing plain scalars to the visitor as written. (0013)
