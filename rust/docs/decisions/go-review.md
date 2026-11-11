# Review of the design against Go (PGMI-830)

Go v0.14.x (`542e6149`) read and run against the black-box suite, 2026-11-10.
The three research inventories of 2026-11-10 (driver, cloud sign-in,
CLI/TUI/MCP) were walked item by item against `docs/design/behaviour-contract.md`
and the records. Each finding: Go behaviour (where in Go), the contract
section it went into, the case (or why none). Items both already covered are
left out. Open questions 1-8 of the README are answered there and in their
records; their findings are rows 1-15 here.

## Added to the contract

| # | Go behaviour | Go source | Contract section | Case |
|---|---|---|---|---|
| 1 | Symbolic links never followed (exit 10 unless ignored); `deploy.sql` may be one | `internal/files/filesystem/os.go:55` | Project discovery | `dialect/project-walk` |
| 2 | NUL byte or invalid UTF-8 in a file is exit 10; offset given for `deploy.sql` | `internal/files/scanner/scanner.go:271,274,361,364` | Project discovery | `dialect/project-walk` |
| 3 | `__name__` directory other than `__test__`/`__tests__` is exit 10 | `scanner.go:387` | Project discovery | `commands/metadata-refusals` |
| 4 | `PGMI_MAX_FILE_SIZE` caps a file, must be a positive integer | `filesystem/os.go:19-28,62` | Project discovery | `commands/metadata-refusals` |
| 5 | Comments allowed between macro tokens; ASCII `\s` only | `internal/preprocessor/macros.go:35`, `comments.go:44` | Test macro | `dialect/pgmi-test-macro-comments`, `dialect/pgmi-test-macro-whitespace` |
| 6 | `.env` expands `$NAME` from earlier lines only; broken `.env` warns, applies nothing | `internal/cli/config_common.go:243-282` (godotenv) | File formats | `formats/dotenv-expansion` |
| 7 | `.env` enters the process environment, so it reaches cloud sign-in | `config_common.go:257` | File formats, Cloud sign-in | `cloud/dotenv-feeds-sdk` |
| 8 | Pager split on white space, no shell, default `less -R`, failing pager prints directly | `internal/ui/pager.go:14-60` | Terminal behaviour | `terminal/pager-command` |
| 9 | Bare `pgmi`: help on stdout, banner on a terminal | `internal/cli/root.go:38-58` | templates, version, ... | `commands/bare` |
| 10 | Summary line: `%.2fs`, ignored count, what was left on failure | `internal/cli/deploy.go:554-591` | Notices and the summary line | `notices/summary-line` |
| 11 | Metadata: four id spellings, `ParseBool`, lenient XML, text after root ignored | `internal/metadata/types.go:29-33`, `extractor.go:58-129` | metadata | `commands/metadata-forms` |
| 12 | Metadata refusals (missing idempotent, nil id, blank key/description, entity, two blocks, 10240 bytes) | `metadata/validator.go:26-55`, `extractor.go:16,28` | metadata | `commands/metadata-refusals` |
| 13 | Fallback id `md5("./<path>")` as uuid, same in plan and session | `metadata/identity.go:17`, `shared/contract/api-v1.sql` `generic_id` | metadata | `commands/metadata-forms` |
| 14 | Checksum trim is Unicode White_Space at the ends only | `internal/checksum/calculator.go:52` | Checksums | `checksums/golden` |
| 15 | Go embeds only named `shared/` paths | `shared/shared.go:8-40` | none (build fact) | none: embedded set not visible from outside; checked by file list |
| 16 | `-v` prefixes every notice line with `[1.23s] ` | `deploy.go:497-509` | Verbose output | `notices/verbose-elapsed` |
| 17 | `pgmi: error:` prefix; unknown subcommand/shorthand = 2; `-h` on deploy explained; several cloud flags = 10; `PGMI_PARAM_` with no name = 10; `--param` not split; `=false`; `--timeout 0` | `cmd/pgmi/main.go:24`, `internal/db/resolver.go:226`, `internal/params/env.go:26` | Exit code table, Parameters | `commands/usage-errors` |
| 18 | `{{PROJECT_NAME}}` replaced, JSON-escaped in `.json` | `internal/scaffold/scaffold.go:227` | init | `commands/init-project-name` |
| 19 | EOF at the overwrite prompt = 12, SIGINT at prompt or countdown = 130, Ctrl-C in `config` = 130 | `internal/ui/interactive_approver.go:81`, `forced_approver.go:50` | Target database ..., config | `terminal/prompt-cancel` |
| 20 | No terminal query outside wizards | `cmd/pgmi/terminal_query_test.go` (Go's own guard) | Terminal behaviour | `terminal/no-terminal-queries` |
| 21 | Wizards/pager need stdin+stdout on a terminal, prompts stdin+stderr | `internal/tui/detect.go:46,59` | Terminal behaviour | none: the runner's pty holds all three streams |
| 22 | serve malformed input: -32700/-32600/-32602 rules, null ids, blank lines, 2 MB line, invalid UTF-8 | `internal/mcp/server.go:110-170`, `protocol.go:97-113` | serve | `serve/malformed` |
| 23 | serve protocol negotiation, 2026-07-28 stateless era, `server/discover`, -32022 | `protocol.go:5-50`, `server.go:312-366` | serve | `serve/protocol-versions` |
| 24 | serve deploy: no `.env`, `confirmDatabaseName` (12), unknown argument (10) | `internal/cli/serve.go:341-345,458-475,517-531` | serve | `serve/deploy-guards` |
| 25 | serve deploy `timeout` argument (16; not a duration = 10) | `serve.go:30,363-367` | serve | `serve/deploy-timeout` |
| 26 | serve deploys are independent: open transaction or failed tail block leaves no lock; overlap = 15 | `serve.go:477-496` (one deployer per call) | serve | `serve/deploys-in-sequence`, `serve/concurrent-deploys` |
| 27 | `max_protocol_version=3.2` works, cancel too; `sslsni`, `sslcertmode`, `client_encoding` accepted | pgx (driver) | Connection resolution | `connection/protocol-version` |
| 28 | Azure default chain ends with Azure PowerShell; `AZURE_TOKEN_CREDENTIALS` selects | azidentity v1.15 `default_azure_credential.go:57-228` | Cloud sign-in; record 0007 updated | `cloud/azure-powershell` |
| 29 | Refused cloud token: exit 11 naming the user, pointing at `--username` | `internal/db/connector.go:309` | Cloud sign-in | `cloud/dotenv-feeds-sdk` |
| 30 | Default sslmode: Azure `verify-full`, AWS `require` or `verify-full` with a root | `resolver.go:294-326` | Cloud sign-in | none: the fakes are reached only with an explicit sslmode |
| 31 | serve has no cloud arguments; cloud only via `pgmi.yaml` and environment | `serve.go:220-235` | serve | none: no fake reachable without arguments |
| 32 | JSON escapes `<`, `>`, `&`; two-space indent | `encoding/json` defaults | The --json record (bytes not contract) | none: deliberately not pinned |

## Owner questions (Go misbehaves or Rust cannot match cheaply)

Each is recorded, not encoded as a case, because the case would pin Go's
behaviour and the contract should not require it.

| # | Go behaviour | Evidence | Proposal |
|---|---|---|---|
| Q1 | The line `null` gets no answer (JSON-RPC 2.0 wants -32600, null id) | `server.go:121` unmarshals `null` into the zero request, which then reads as a notification | Rust answers -32600 with a null id; flip in `serve/malformed` once decided |
| Q2 | An id holding invalid UTF-8 is echoed as raw bytes: the response is not valid JSON | `serve_probe` run, `server.go:160-170` | Rust decodes each line lossily (invalid UTF-8 becomes U+FFFD, as Go does inside strings and `serve/malformed` requires), so this id is echoed with U+FFFD as valid JSON; intended change |
| Q3 | libpq keys pgx does not implement go to the server as settings and fail, exit 11: `keepalives*`, `tcp_user_timeout`, `load_balance_hosts`, `gssencmode`, `hostaddr` (URI form), `sslcrl`, `requirepeer` and others in pgmi's libpq list | probe on Go: `unrecognized configuration parameter "keepalives"` | Rust implements those tokio-postgres has (keepalives, `tcp_user_timeout`, `load_balance_hosts`, `hostaddr`) and refuses the rest with exit 10; extends the decided "unknown keys" change |
| Q4 | `min_protocol_version=3.2`: Go speaks 3.2; tokio-postgres only 3.0 | `connection/protocol-version` passes on Go | Rust accepts both keys; with `min_protocol_version=3.2` it refuses (exit 10) until the layer speaks 3.2 (record 0004) |
| Q5 | Esc on the first `config` screen did not end the wizard under the runner's pty | `terminal/prompt-cancel` first draft hung | Resolved 2026-11-11: the runner sent the Esc byte followed by a newline, which both binaries read as another key. Sent alone, Esc cancels with exit 12 on Go and Rust (`terminal/config-esc`); owner confirmed Esc must cancel |
| Q6 | `(?i)` makes `CALL pgmi_teſt()` (U+017F) a macro in Go | research probe | Not pinned; Rust `regex` with `(?i)` folds the same way. Harmless either way |

## Deliberately dropped

| Go behaviour | Reason |
|---|---|
| Library deploy path may follow `PGSERVICE` | The Go library is dropped at switch day; the CLI refuses it (`env/pgservice-refused`) |
| Help, error and completion text byte for byte | No case greps them beyond key words; clap's layout accepted (record 0011) |
| Walk order of the scanner (lexical) | Not observable: plan order is sort key then path |
| Token expiry warning under 5 minutes, fresh token per dial, `MaxConnLifetime` cap | Kept in records 0007/0008; not observable through the fakes (tokens are cached or the dial is refused) |
| File modes 0644/0755 written by `init` | Kept (record 0010); the runner checks content only |
