# 0020: `<pgmi-meta>` blocks and the metadata commands

Status: accepted, 2026-11-10 (owner delegated, see PGMI-829). Ticket: PGMI-829. Not in the required list;
added because the XML reader is a dependency choice of its own.
Checked on crates.io, 2026-11-10: quick-xml 0.42.0 (2026-08-22), roxmltree
0.21.2 (2026-10-24, read-only tree, no dependencies), uuid 1.29.2.

## Context

A SQL file may start with a `/* <pgmi-meta id=".." idempotent=".."> ...
</pgmi-meta> */` block with `<description>` and `<sortKeys>`. pgmi parses it
before deploying and in `metadata plan|validate|scaffold`; a bad or duplicate
id is exit 10 naming every file; `validate` warns about a block in a `--`
comment; `scaffold` writes blocks with new random ids and, from now on, stops
its project-root search at the first folder holding `.git` (intended change).
No XSD is validated (intended change).

## Options

1. **quick-xml**: streaming, fast, more code to assemble a small element.
2. **roxmltree**: parses a whole document into a read-only tree and rejects
   malformed XML; the blocks are tiny, so a tree is the simplest fit.
3. **Hand-written**: no dependency, but XML escaping and entities are easy to
   get subtly wrong.

## Decision

roxmltree (the research had proposed quick-xml; a tree is less code here).

- The block is found with the lexer of record 0014 (the first block comment
  containing `<pgmi-meta`), then parsed by roxmltree. Unknown elements and
  attributes are ignored; a missing `<description>` is the empty string.
- Ids: parsed with `uuid`, accepting only the hyphenated 36-character form.
  `scaffold` makes v4 ids.
- `scaffold --write` writes each file through a temporary file and a rename
  and names it on stderr; it prints `Project root: <path>` when the root
  differs from the named folder. A new black-box case is added with it.

## Consequences

- Open questions that need Go to answer, each one observable:
  - Which id spellings does Go accept (braces, `urn:uuid:`, no hyphens,
    upper case)?
  - How strict is Go's XML reading (unknown elements, entities, a second
    block)?
  - How is the "deterministic fallback identifier" of a file without
    metadata made? If the binary computes it, it must be byte-exact like a
    checksum, and needs a golden case.

## Serves

metadata; metadata scaffold stops at the repository root; Session API (plan
order, `<description>`); Nothing that cannot be reached is carried over (no
XSD).
