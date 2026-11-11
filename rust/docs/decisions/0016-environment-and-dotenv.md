# 0016: Environment and `.env`

Status: accepted, 2026-11-10 (owner delegated, see PGMI-829). Ticket: PGMI-829.
Checked on crates.io, 2026-11-10: dotenvy 0.15.7 (last release 2023-03-22).
Rust 2024 makes `std::env::set_var` `unsafe`: it is only sound while no other
thread can read the environment.

## Context

pgmi reads many variables: `PGMI_*`, `PG*`, `DATABASE_URL`, `CI`,
`NO_COLOR`, the pager, and the cloud SDKs read their own (`AZURE_*`,
`AWS_*`, `GOOGLE_APPLICATION_CREDENTIALS`, `IDENTITY_ENDPOINT`). On `deploy`
the project `.env` is loaded first and never overrides a variable already
set; `--json` `dotenvKeys` lists the keys it did set. `serve` never reads a
`.env`. `.env` and `--params-file` share one syntax; a params file also
refuses a backslash or `$` inside double quotes and an unquoted ` #`
(exit 10). Both have cases with exact expected values.

## Options

- **Parser**: dotenvy (substitutes variables with its own rules, cannot
  enforce the params-file refusals, no release since 2023) or pgmi's own
  parser with a strict mode. Own parser, about 150 lines.
- **How `.env` values reach pgmi and the SDKs**:
  1. Keep everything in a map and never touch the process environment. pgmi
     code is fine, but the cloud SDKs read the real environment, so an
     `AZURE_CLIENT_SECRET` in `.env` would silently stop working.
  2. Export the new keys with `set_var` before any thread exists, and also
     keep them in the map.

## Decision

Own parser; option 2.

- `main` is synchronous until the runtime starts (record 0003). In order:
  snapshot `std::env::vars_os()` into an `Env` map (in `main`, once); parse
  arguments (`pgmi::run`); for `deploy`, read `<project>/.env`, add the keys
  not already set to the map, and export exactly those with `set_var` (one
  `unsafe` block in `main`, handed to the library as the `export` closure,
  with the invariant written next to it); then start the runtime. After that point
  nothing calls `set_var` or `remove_var`.
- All pgmi code reads the `Env` map, passed down explicitly; nothing reads
  `std::env::var` directly. Tests build an `Env` by hand.
- On Windows the map looks names up case-insensitively, as the OS does.
- A non-UTF-8 value of a variable pgmi reads is exit 10, naming the
  variable. An empty `PGMI_PARAM_*` is exit 10.
- **Parser**: `KEY=VALUE`, optional `export`, whole-line `#` comments,
  whitespace around key and value trimmed, only the first `=` splits,
  single quotes literal, double quotes with escapes (`\n`, `\"`, `\\`) and
  newlines, an unquoted ` #` starts a comment in `.env`, CRLF accepted.
  In `.env`, `$NAME` and `${NAME}` in unquoted and double-quoted values
  expand from keys earlier in the file, else to empty.
  Strict mode (params files) refuses `\` or `$` inside double quotes and an
  unquoted ` #`, exit 10, naming the line and suggesting single quotes.
  Keys are lower-cased for params; two keys in one file differing only in
  case is exit 10.

## Consequences

- Answered from Go (PGMI-830): `.env` expands `$NAME` and `${NAME}` in
  unquoted and double-quoted values, never in single quotes, and only from
  keys earlier in the same file; a name that is only in the real
  environment, or nowhere, expands to empty. So the `.env` reader expands;
  the strict params-file reader still refuses `$`
  (`formats/dotenv-expansion`). A `.env` that does not parse applies nothing
  and prints the fixed WARNING naming the file (same case).
- Answered from Go: `.env` does feed the cloud SDKs; AWS keys and region in
  `.env` sign the RDS token (`cloud/dotenv-feeds-sdk`). Exporting the keys
  before any thread exists, as above, keeps that. `pgmi serve` never reads
  `.env` (`serve/deploy-guards`).

## Serves

Parameters; File formats; Connection sources; Terminal behaviour; The --json
record (`dotenvKeys`); serve (no `.env`); Cloud sign-in.
