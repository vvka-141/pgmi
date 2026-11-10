# 0014: `.pgmiignore` matching and the SQL lexer

Status: accepted, 2026-11-10 (owner delegated, see PGMI-829). Ticket: PGMI-829.
Checked on crates.io / docs.rs, 2026-11-10: ignore 0.4.34 and globset 0.4.21
(2026-10-27). globset's documented syntax has `{a,b}` alternation, `[ab]`,
`[!ab]`, and no POSIX classes such as `[[:space:]]`. regex 1.13.1 (not
needed by any choice below).

## Context

`.pgmiignore` has gitignore semantics: comments, globs, classes, `**`,
directory patterns, `!`, escapes, and (intended change) POSIX classes with
git's meaning. The `dialect/pgmiignore` case pins classes containing `&&`
and `--`, which Rust's regex reads specially. A pattern set that hides the
root `deploy.sql` is exit 10.

Several features read SQL text: the head/tail split of `deploy.sql`, finding
`CALL pgmi_test(...)` (never inside comments, strings or dollar quotes), the
checksum normalisation (record 0015), finding the `<pgmi-meta>` block
comment (record 0020), and placing an error on a line.

## Options

- **`.pgmiignore`**: the `ignore` crate (ripgrep's gitignore matcher on
  globset: brace alternation git does not have, no POSIX classes); globs
  translated to regex as Go does (the class differences the case pins); or
  pgmi's own matcher following git's rules.
- **SQL**: a parser crate (sqlparser-rs: a full grammar that rejects
  PostgreSQL syntax it does not know, and normalises text pgmi must keep
  byte for byte), or one hand-written lexer.

## Decision

**Own gitignore matcher**, written from `gitignore(5)` and git's documented
wildmatch rules, not ported from git's code (git is GPL-2; pgmi is MPL-2.0).
About 300 lines: `*`, `?`, `[...]` with `!`/`^` negation, ranges, the twelve
POSIX classes (ASCII, case-sensitive), `\` escapes, `**` in its three
positions, trailing `/` for directories, a leading or middle `/` anchoring
to the project root, unescaped trailing spaces trimmed, `!` re-inclusion
(never inside an ignored directory, which is not walked). Test vectors are
written for pgmi plus the black-box cases. No regex crate.

The walk: `std::fs::read_dir`, sorted, built-in exclusions first (root
`deploy.sql` from the source set, `__test__/` and `__tests__/` to the test
set, dot-names, `node_modules/`, `__pycache__/`), then `.pgmiignore`. Every
file: BOM stripped, size cap 10 MiB or `PGMI_MAX_FILE_SIZE` (a whole number
of bytes, else exit 10). `-v` lists each ignored path and its rule.

**One hand-written SQL lexer** over bytes, producing tokens with their byte
ranges: whitespace, `--` comment, nested `/* */` comment, `'...'` with `''`,
`E'...'` with backslash escapes (only when `E`/`e` stands alone, not ending
an identifier), `U&'...'`, `"..."` with `""`, `$tag$...$tag$` (tag starts
with a letter or `_` and does not continue an identifier), words, `;`,
parentheses, other. On top of it:

- **Splitter**: top-level statements; the first top-level terminator
  (`COMMIT`, `END`, `ROLLBACK`, `ABORT`, with `WORK`, `TRANSACTION`,
  `AND [NO] CHAIN`) ends the head. Not terminators: `ROLLBACK TO
  [SAVEPOINT]`, `COMMIT PREPARED`, `ROLLBACK PREPARED`, and the `END` of a
  `BEGIN ATOMIC` body, which is stepped over whole.
- **Macro finder**: `CALL`, optional `pg_temp.`, `pgmi_test`, `(`, zero to
  two string literals separated by `,`, `)`, in any letter case with
  whitespace between tokens; replaced by the SQL that
  `pgmi_test_generate()` returns, with a line map kept for error placement.

## Consequences

- The lexer is shared, so a change for one reader can break another; the
  checksum golden case and the cross-binary cases guard it.
- Open questions for the owner, needing Go to answer: does the walk follow
  symbolic links? How is a "binary file" detected (a NUL byte, invalid
  UTF-8?) and with which exit code? Are comments allowed between the
  macro's tokens?

## Serves

Project discovery and loading; POSIX character classes in .pgmiignore;
Execution contract; Test macro; Error attribution; Verbose output (ignored
paths).
