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
- Ids: parsed with `uuid::Uuid::parse_str`, which accepts the four forms
  Go accepts (hyphenated, braced, `urn:uuid:`, 32 hex digits, any case,
  any version) and nothing else (a leading space is refused); stored
  lower-case hyphenated. `scaffold` makes v4 ids.
- `scaffold --write` writes each file through a temporary file and a rename
  and names it on stderr; it prints `Project root: <path>` when the root
  differs from the named folder. A new black-box case is added with it.

## Consequences

- Answered from Go (PGMI-830), cases `commands/metadata-forms` and
  `commands/metadata-refusals`:
  - Id spellings: the four above.
  - XML: unknown elements and attributes, a prolog and a default namespace
    are ignored (match on the local name); entities, character references
    and CDATA decode; an unknown entity (`&nbsp;`) is exit 10; repeated
    `<sortKeys>` merge; `idempotent` is Go's `ParseBool` after trimming
    (`1`, `t`, `T`, `TRUE`, `true`, `True`, and the false forms), required;
    two blocks are exit 10; the block is capped at 10240 bytes.
  - **Text after the root element is ignored.** roxmltree rejects it as
    malformed, so pgmi hands roxmltree only the text up to the end of the
    root element (found with roxmltree's own error position, or by parsing
    `<pgmi-meta` .. `</pgmi-meta>`), or this record falls back to
    quick-xml's reader, which stops after the root. A second `<pgmi-meta`
    after the root, in the same comment, is not ignored: it counts as a
    second block, exit 10 (PGMI-868, a decided change from Go).
  - The fallback id is `md5("./<path>")` read as a uuid, computed by the
    binary for `metadata plan` and by the session SQL for
    `pgmi_plan_view.generic_id`; both are pinned by `commands/metadata-forms`.

## Serves

metadata; metadata scaffold stops at the repository root; Session API (plan
order, `<description>`); Nothing that cannot be reached is carried over (no
XSD).
