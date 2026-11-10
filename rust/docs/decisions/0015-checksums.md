# 0015: Checksums

Status: accepted, 2026-11-10 (owner delegated, see PGMI-829). Ticket: PGMI-829.
Checked on crates.io, 2026-11-10: sha2 0.11.1 (2026-10-12, rust-version
1.88), ring 0.17.14 (already in the build for TLS).

## Context

`checksum` is the hex SHA-256 of a file's bytes after the BOM is stripped.
`pgmi_checksum` is the hex SHA-256 of the normalised text. Both must be byte
for byte what Go produces: the templates compare them to decide whether a
run-once file was edited, and a database deployed by Go must redeploy with
Rust without drift. `checksums/golden` holds 17 golden pairs;
`checksums/cross-binary-redeploy*` redeploy a Go-deployed database.

## Options

- **Hash**: sha2 (pure Rust, independent of the TLS choice) or ring's
  digest (already compiled). sha2: the checksum should not move if the TLS
  provider changes.
- **Normaliser**: on the shared lexer of record 0014, or a separate state
  machine. The shared lexer: one definition of a string, comment and dollar
  quote, guarded by the golden case.

## Decision

sha2, lower-case hex, and a normaliser over the lexer's tokens, working on
bytes:

1. Strip a leading UTF-8 BOM (done once at load, record 0014).
2. Each comment (`--` to end of line, nested `/* */`) becomes one space.
3. Outside literals, each run of ASCII white space (space, tab, CR, LF,
   VT, FF) becomes one space, the spaces from step 2 included, so
   `id /* x */ FROM` equals `id FROM`. Rust's `u8::is_ascii_whitespace`
   leaves out VT, so the set is written out by hand.
4. Outside literals, ASCII letters are lower-cased; other bytes are kept.
5. String literals, `E''` strings, `U&''` strings, quoted identifiers and
   dollar-quoted bodies are copied unchanged.
6. Leading and trailing Unicode white space is trimmed: the Unicode
   `White_Space` property, which is what Rust's `char::is_whitespace` tests
   and, by its definition, Go's `unicode.IsSpace` set too.

## Consequences

- The golden case runs in CI against the Rust binary from the first day,
  and the cross-binary cases against the Go reference until switch day.
- A file that is not valid UTF-8 is hashed as bytes; what happens to it
  when loaded is the open question in record 0014.
- Open question: no golden vector has non-ASCII white space at either end
  (U+00A0, U+3000). Add one from the Go binary before switch day so step 6
  is pinned, not assumed.

## Serves

Checksums; Template deployments; Session API.
