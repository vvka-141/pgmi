---
name: pgmi-metadata
description: Explains pgmi-meta blocks, the XML header in a pgmi project's SQL files that carries a script id, an idempotent flag and sortKeys. Covers how pgmi turns them into the order of pgmi_plan_view, multi-phase keys, file identity across renames, and the pgmi metadata plan, validate and scaffold commands. Use when adding or editing a pgmi-meta block, choosing a sort key, when a file runs in the wrong order, twice, or not at all, when a deploy fails on a metadata error or duplicate id, or before renaming, moving or copying a SQL file that has one.
---

# pgmi-meta blocks

A pgmi-meta block is an XML header inside a SQL file's block comment. pgmi
parses and validates it before connecting, then publishes it in the session:
in the metadata view and as the order of `pgmi_plan_view`. pgmi runs nothing
because of it. `deploy.sql` decides whether sort keys set the order and
whether `idempotent` changes anything.

## First: does this project's deploy.sql read them?

Search `deploy.sql` for `pgmi_plan_view` and `idempotent`.

- It loops over `pgmi_plan_view` ordered by execution order: sort keys set
  the order.
- It loops over `pgmi_source_view` with an `ORDER BY` of its own (path, for
  example): sort keys are ignored and file names set the order. A block still
  has to be valid and its id unique, because pgmi checks every SQL file.
- `idempotent` matters only where `deploy.sql` reads it, typically to skip a
  file it has recorded as already run. Read that code before picking a value.

Adding a block to a project whose `deploy.sql` ignores the plan changes
nothing. Change `deploy.sql`, or rename the file. To make a `deploy.sql`
follow the plan, see [references/deploy-sql.md](references/deploy-sql.md).

## The block

```sql
/*
<pgmi-meta id="4b0c4f63-7f0e-4e7a-9a52-2f1c3d6b8e10" idempotent="true">
  <description>Order lookup functions</description>
  <sortKeys>
    <key>030/010</key>
  </sortKeys>
</pgmi-meta>
*/
CREATE OR REPLACE FUNCTION ...
```

pgmi rejects the deploy with exit 10, before connecting and naming the file,
when:

- `id` is missing, not a UUID, or the nil UUID;
- `idempotent` is missing or not a boolean;
- a `<key>` is empty or whitespace, or the XML does not parse;
- a file has two blocks;
- two files share an `id`.

Give every new file a fresh random UUID (`uuidgen`, or
`SELECT gen_random_uuid()`).

What pgmi does not check:

- Unknown attributes and elements are ignored without a word. A
  `sortKey="..."` attribute does nothing; keys are `<key>` elements inside
  `<sortKeys>`.
- A block in a `--` line comment, in a non-SQL file, or in a file under
  `__test__/` or `__tests__/` is not read at all.

Check any block you write with `pgmi metadata plan .`.

## How the plan is ordered

- Each `<key>` gives the file one plan row. A file with no block, or with a
  block but no keys, gets one row keyed by its path (`./dir/file.sql`).
- Rows sort by key, then by path, in byte order (`COLLATE "C"`). The order is
  the same on every server, whatever its locale.
- Keys are strings: `10` sorts before `9`. Pad numbers to a fixed width.
- A path key starts with `./`, and `.` sorts before digits and letters. So
  files keyed by path run before files whose keys start with a digit or a
  letter.
- Files with equal keys run in path order.
- The plan holds every loaded file, `README.md` included. The loop in
  `deploy.sql` must keep only SQL files.

To place a new file, run `pgmi metadata plan .`, find the files that must run
before and after it, and choose a key between them in the scheme the project
already uses. Do not start a second scheme beside the existing one.

## Several keys run the whole file several times

A file with two keys gets two plan rows, and a `deploy.sql` that executes the
plan runs the entire file at each one. It does not run "the schema part" at
the first key and "the data part" at the second. Every statement must be
correct at every position. A file that creates a schema and then seeds a
table created by a later file fails at its early key, because the table does
not exist yet.

Several keys are for re-run files (`idempotent="true"`) only. A run-once file
runs once, at one place in the plan, and both templates' `deploy.sql` refuse
a run-once file with more than one key before anything runs.

Default: one file per stage. Use several keys only for a file that is safe to
re-run in full at each position, such as re-applying grants after later
objects exist.

## Identity

- `id` identifies the file independently of its path. Moving or renaming the
  file keeps the id, so a `deploy.sql` that tracks applied files by id still
  recognises it.
- A file without a block gets a fallback id in the plan: the MD5 of its path
  string (case-sensitive, with the `./` prefix). Moving, renaming or recasing
  the file changes it, and a tracker keyed on it sees a new file.
- Copying a file copies its id, and the deploy fails with
  `duplicate <pgmi-meta id> across files` (exit 10). Give the copy a new id.
- Never give a file's id to different content. To a tracker keyed on ids,
  changing the id of a file that already ran means one file was removed and
  another added. Check `deploy.sql` before you change one.

## Commands

None of these opens a database connection.

| Command | Does |
|---|---|
| `pgmi metadata plan .` | Lists the plan rows in order, one per key, non-SQL files marked. `--json` for tooling |
| `pgmi metadata validate .` | Parses every block and reports duplicate ids. Exits 10 on failure. `--json` |
| `pgmi metadata scaffold <dir> --sort-key <prefix>` | Previews a block for each SQL file outside the test directories that has none. `--write` writes them |

`scaffold` is a starting point. Each block it writes has:

- a new random id;
- `idempotent="true"`, or `false` for every file with `--idempotent=false`;
- one key, `<prefix>/<file name>`. The prefix is `--sort-key`, or else the one
  prefix the files with metadata in the same directory already share; with
  neither, it refuses (exit 2) and writes nothing.

The prefix is never taken from the path: directories do not run in
alphabetical order. Scaffold one directory at a time with the prefix that
puts it where it belongs, then check the order with `pgmi metadata plan .`
and set `idempotent` file by file wherever `deploy.sql` reads it.

## Test the order you depend on

A test can assert the plan. This one holds in any project: every file with a
block has exactly one plan row per key, or one row when it has none. It is
verified to deploy as written, as `__test__/test_pgmi_metadata_1.sql`.
For a project-specific test, compare the plan positions of two named paths.

```sql runnable target=__test__/test_pgmi_metadata_1.sql
DO $$
DECLARE
    v_mismatch text;
BEGIN
    SELECT string_agg(m.path, ', ' ORDER BY m.path) INTO v_mismatch
    FROM pg_temp.pgmi_source_metadata_view AS m
    WHERE (SELECT count(*) FROM pg_temp.pgmi_plan_view AS p WHERE p.path = m.path)
          IS DISTINCT FROM greatest(cardinality(m.sort_keys), 1);

    IF v_mismatch IS NOT NULL THEN
        RAISE EXCEPTION 'plan rows do not match sort keys for: %', v_mismatch;
    END IF;
END $$;
```

For the session views and their columns, run `pgmi ai contract`. For the
deploy-and-test loop, see the pgmi-workflow skill.
