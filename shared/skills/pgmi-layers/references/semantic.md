# The semantic layer

Views, writer functions and refusal functions: what the data means and how it
may change. All of it lives in re-run files (`CREATE OR REPLACE`), so every
deploy can change it.

## Contents

1. Writers
2. Refusal functions: one rule, one place
3. Views
4. Compute once
5. A runnable example

## 1. Writers

- The only code that changes rows. A caller, a job, a seed and a test
  fixture all go through them, so a rule is checked on every path.
- Return the affected row type (`RETURNS document`, `SETOF document`), so the
  caller reads the result from the same snapshot.
- `LANGUAGE sql` when one statement fits. Answer NULL when the rule
  refuses (`RETURNS document` gives one row of NULLs, so callers test
  `(f(...)).document_id IS NULL`; `SETOF document` gives no row); do not raise to say "not found" or "not allowed".
- A state transition's `UPDATE` also names the state it moves from
  (`AND status = 'open'`). After a lock wait PostgreSQL re-checks only the
  row's own columns, so that predicate is the race guard.
- A `STABLE` function called in the same statement as the writer reads the
  snapshot from before the write. Check what a writer did in the next
  statement.
- The constraint is still the last word. A refusal function that checks for
  a duplicate cannot see a concurrent insert; the unique index can. The
  writer's check gives the reason, the constraint gives the guarantee.

## 2. Refusal functions: one rule, one place

Names, in the entity's schema: writers `fn_<verb>_<entity>`, refusal
functions `fn_<entity>_problem` and `fn_<entity>_problems`. A code is
`<schema>.<entity>.<reason>`; for objects in `public` the schema is omitted
(`task.not_found`).

**A refusal of an operation** is
`fn_<entity>_problem(p_id, p_operation text) RETURNS text`: plain SQL, a
`CASE` over views, answering a code or NULL. Its first arm is
`<schema>.<entity>.not_found`, so one call tells a missing row from a
forbidden operation.

**Field validation** is `fn_<entity>_problems(...) RETURNS TABLE (field text,
reason text)`: a `VALUES` list of `(field, reason-or-NULL)` filtered to the
non-NULL rows, so the caller gets every problem at once. The writer writes
`WHERE NOT EXISTS (SELECT FROM fn_<entity>_problems(...))`.

- A code is dotted, `<schema>.<entity>.<reason>`
  (`pgmi_layers.document.has_open_reviews`); a field's reason is one word
  (`required`, `duplicate`, `not_found`), since the field already names the
  thing. Both are stable machine words, never sentences. Wording is the
  caller's.
- A value that clashes with stored data (a title already taken) is a field
  problem, reason `duplicate`; a state that forbids the action is an
  operation problem.
- When a caller must know in advance, the view carries the same function as
  a column (`delete_problem`); the caller hides the action and the writer
  still refuses.
- Before adding a check, a status literal or a flag, search the writers and
  views for the same rule. If it is there, move it into one of these
  functions and point every reader at it.

## 3. Views

A view adds how a thing is used to its table: counts, latest dates, states.

- **Grain**: one row per what the view is named for; facts aggregated at
  that grain, never a global figure repeated on every row.
- **Joins carry meaning**: an optional fact is a `LEFT JOIN` or a scalar
  subquery, so a missing counterpart never drops a row; an inner join is a
  deliberate filter.
- **Facts cost nothing unselected**: a scalar subquery in the select list is
  evaluated only when a query selects that column.
- **A state is a column**, computed once; a filter on the state reads the
  column.
- **`CREATE OR REPLACE VIEW` only adds columns at the end.** It cannot drop,
  rename or retype one; that change is `DROP VIEW` and `CREATE VIEW` in the
  same re-run file, and every view built on it goes too.
- **Earn the view**: name the use and how it differs from every existing
  view in grain, filter or cost. If you cannot, extend the existing one.

## 4. Compute once

A value computed again and again inside one statement is invisible in a
one-row test and grows with the rows.

- Wrap a check whose arguments are constant for the statement as
  `(SELECT fn(...))`: PostgreSQL runs it once, as an InitPlan, instead of
  per row. Row policies need this most.
- Hand a function the row, or the columns it needs, not the id it would look
  up again.
- One `LEFT JOIN LATERAL` feeds several columns; never call one function once
  per column of the same row.
- A page computes facts for the rows it shows: cut the page first, then add
  the facts.
- Count calls to prove it: `SET track_functions = 'pl'` (or `all`) and read
  `pg_stat_xact_user_functions` around the statement.

## 5. A runnable example

The fixture builds both layers in a schema of their own: two tables with
constraints, a view, a refusal function per kind and two writers. It is
deployed as a test fixture so it rolls back; in a project the tables go in a
run-once file and the rest in re-run files.

```sql runnable target=__test__/pgmi_layers/_setup.sql
CREATE SCHEMA pgmi_layers;

CREATE TABLE pgmi_layers.document (
    document_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title       text NOT NULL CONSTRAINT document_title_check CHECK (btrim(title) <> '')
);
CREATE UNIQUE INDEX document_title_key ON pgmi_layers.document (lower(title));

CREATE TABLE pgmi_layers.review (
    review_id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id bigint NOT NULL REFERENCES pgmi_layers.document ON DELETE CASCADE,
    note        text NOT NULL,
    status      text NOT NULL DEFAULT 'open' CONSTRAINT review_status_check CHECK (status IN ('open', 'done'))
);
CREATE INDEX review_document_id_idx ON pgmi_layers.review (document_id);

CREATE VIEW pgmi_layers.document_view AS
SELECT d.document_id, d.title,
       (SELECT count(*) FROM pgmi_layers.review r
         WHERE r.document_id = d.document_id AND r.status = 'open') AS open_review_count
FROM pgmi_layers.document d;

CREATE FUNCTION pgmi_layers.fn_document_problem(p_document_id bigint, p_operation text)
RETURNS text LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN d.document_id IS NULL                                  THEN 'pgmi_layers.document.not_found'
        WHEN p_operation = 'delete' AND d.open_review_count > 0     THEN 'pgmi_layers.document.has_open_reviews'
    END
    FROM (SELECT p_document_id) k
    LEFT JOIN pgmi_layers.document_view d ON d.document_id = p_document_id;
$$;

CREATE FUNCTION pgmi_layers.fn_document_problems(p_title text)
RETURNS TABLE (field text, reason text) LANGUAGE sql STABLE AS $$
    SELECT field, reason FROM (VALUES
        ('title', CASE WHEN p_title IS NULL OR btrim(p_title) = '' THEN 'required'
                       WHEN EXISTS (SELECT FROM pgmi_layers.document d
                                    WHERE lower(d.title) = lower(btrim(p_title))) THEN 'duplicate' END)
    ) AS p(field, reason)
    WHERE reason IS NOT NULL;
$$;

CREATE FUNCTION pgmi_layers.fn_create_document(p_title text)
RETURNS pgmi_layers.document LANGUAGE sql AS $$
    INSERT INTO pgmi_layers.document (title)
    SELECT btrim(p_title)
    WHERE NOT EXISTS (SELECT FROM pgmi_layers.fn_document_problems(p_title))
    RETURNING *;
$$;

CREATE FUNCTION pgmi_layers.fn_delete_document(p_document_id bigint)
RETURNS pgmi_layers.document LANGUAGE sql AS $$
    DELETE FROM pgmi_layers.document
    WHERE document_id = p_document_id
      AND (SELECT pgmi_layers.fn_document_problem(p_document_id, 'delete')) IS NULL
    RETURNING *;
$$;
```

```sql runnable target=__test__/pgmi_layers/test_pgmi_layers.sql
DO $$
DECLARE
    v_document pgmi_layers.document;
    v_problem  text;
BEGIN
    v_document := pgmi_layers.fn_create_document('Handbook');
    IF v_document.document_id IS NULL THEN
        RAISE EXCEPTION 'the writer must create a valid document';
    END IF;
    IF (pgmi_layers.fn_create_document(' handbook ')).document_id IS NOT NULL
       OR (SELECT reason FROM pgmi_layers.fn_document_problems(' handbook ')) IS DISTINCT FROM 'duplicate' THEN
        RAISE EXCEPTION 'a duplicate title must be refused by the writer and named by the problems function';
    END IF;

    INSERT INTO pgmi_layers.review (document_id, note) VALUES (v_document.document_id, 'Check the dates');
    v_problem := pgmi_layers.fn_document_problem(v_document.document_id, 'delete');
    IF v_problem IS DISTINCT FROM 'pgmi_layers.document.has_open_reviews'
       OR (pgmi_layers.fn_delete_document(v_document.document_id)).document_id IS NOT NULL
       OR NOT EXISTS (SELECT FROM pgmi_layers.document WHERE document_id = v_document.document_id) THEN
        RAISE EXCEPTION 'delete with an open review: want pgmi_layers.document.has_open_reviews and the row kept, got %', v_problem;
    END IF;

    UPDATE pgmi_layers.review SET status = 'done' WHERE document_id = v_document.document_id;
    IF (pgmi_layers.fn_delete_document(v_document.document_id)).document_id IS DISTINCT FROM v_document.document_id THEN
        RAISE EXCEPTION 'with every review done the writer must delete the document and return it';
    END IF;
    IF pgmi_layers.fn_document_problem(v_document.document_id, 'delete') IS DISTINCT FROM 'pgmi_layers.document.not_found' THEN
        RAISE EXCEPTION 'a deleted document must read as pgmi_layers.document.not_found';
    END IF;
END $$;
```

The refused delete returns NULL and leaves the document; the caller that
wants to say why asks `fn_document_problem`, the same function the writer
asked.
