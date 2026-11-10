# 0011: CLI argument parsing, help and error text

Status: accepted, 2026-11-10 (owner delegated, see PGMI-829). Ticket: PGMI-829.
Checked on crates.io, 2026-11-10: clap 4.6.7 (2026-09-14, rust-version
1.85), clap_complete 4.6.11 (2026-09-15); shell-words 1.1.1.

## Context

The flags are documented in `docs/CLI.md`. The cases fix: unknown command,
unknown flag, wrong arity and a bad flag value are exit 2 with the offending
word on stderr; `help <cmd>`, `<cmd> --help` and `--help` exit 0 on stdout;
deploy's help contains `Exit codes:`; `-h` is `--host` on deploy, as in
psql; `--version` prints `pgmi <version> (compat 1)`; completion scripts for
bash, zsh, fish and PowerShell. Go's flags also accept `--force=false` and
`--idempotent=false` (documented), and `--timeout` takes Go durations
(`90s`, `1h30m`).

## Options

1. **clap with derive**, plus clap_complete. The standard; help, suggestions
   and completion come with it. Its help layout differs from cobra's.
2. **A small parser** (lexopt, pico-args). Full control of text, but help,
   completion for four shells and error messages are all hand work.

## Decision

Option 1.

- `disable_help_flag` and a long-only `--help`, so `-h` is free for
  `--host`; `disable_version_flag` and pgmi's own `--version` printing
  `pgmi <version> (compat 1)`. `pgmi version` adds `Commit:` and
  `Platform:` lines.
- Boolean flags take an optional value (`num_args = 0..=1`,
  `require_equals`, `default_missing_value = "true"`) parsed with Go's
  spellings (`1 t T TRUE true True 0 f F FALSE false False`).
- `--timeout` (and `timeout` in pgmi.yaml) uses pgmi's own Go-duration
  parser: optional sign, decimal numbers, units `ns us µs μs ms s m h`,
  sequences such as `1h30m`, bare `0`. A bad value is a clap value error,
  exit 2, naming the value.
- `after_help` on `deploy` (and the root) lists the exit codes from the same
  table `Kind::exit_code()` uses (record 0002).
- Enumerated values (`--template`, `--sslmode`, completion shell) use
  `value_parser` lists, which also feed completion.
- clap's own errors are rendered with the `pgmi: ` prefix and exit 2; usage
  errors found later (non-empty `init` target, `metadata scaffold` without a
  sort key, `config` without a terminal) use `Kind::Usage`, also exit 2.
- Help wording follows `docs/CLI.md`; clap's layout is accepted.

## Consequences

- Answered from Go (PGMI-830): bare `pgmi` prints the root help on stdout
  and exits 0; on a terminal it draws the banner on stderr instead, unless
  `PGMI_NO_BANNER` is set (`commands/bare`). Error prefix, `-h` on `deploy`,
  `--force=false`, `--param` with commas and `--timeout 0`:
  `commands/usage-errors`.
- Help text is not byte-identical to Go's; no case requires it.

## Serves

templates, version, ai, completion, help; Exit code table; info; init;
metadata; config; Timeout, cancellation, interruption (`--timeout`
syntax); Verbose output (`-v`).
