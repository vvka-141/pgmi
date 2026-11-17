# The application as a dataset

In a pgmi project the database holds the application's state and its rules.
Services, jobs, user interfaces and agents are callers: they invoke
operations the database defines. The deploy proves those operations with
tests before it commits, in the same session that created them.

## Design order

1. **Data.** Tables and the constraints that make invalid states
   unrepresentable: `NOT NULL`, `CHECK`, `UNIQUE`, foreign keys, the right
   types (`timestamptz`, `numeric` for money). A rule a constraint can
   express belongs in a constraint, not in code.
2. **Operations.** Every change to the data is a function or procedure
   named by what it does (`fn_complete_task`, `fn_archive_document`). It asks the
   refusal function that states its rule, changes nothing when the rule
   refuses, and does its work set-based. One call is one atomic change. The
   pgmi-layers skill has the shapes.
3. **Tests.** Each operation gets tests for the valid case, each rejected
   input, and the edge values, in a `__test__/` directory next to it. The
   deploy's test gate runs them on every deploy.
4. **Callers.** Only now decide how callers reach the operations. Grant
   them `EXECUTE` on the operations rather than write access to the tables;
   an operation that writes tables its caller cannot touch is
   `SECURITY DEFINER` with a fixed `search_path`.

Starting from the caller ("we need an endpoint for completing tasks") puts rules in
the calling layer, where nothing tests them at deploy time. Start from the
operation ("we need `fn_complete_task(task_id)`").

## Reads

Callers read through views (or set-returning functions) shaped for the
question, so a table can change without breaking every caller.

## Privileges

PostgreSQL grants `EXECUTE` on every new function to `PUBLIC`. Check how this
project's `deploy.sql` handles that before adding an operation, and grant
each role exactly the operations it needs.

## Where the code goes

Which directory holds tables, which holds operations, and how they are
ordered is this project's convention. Read `deploy.sql` and the README, and
put each new file where files of its kind already run.

## Loops

A loop in `deploy.sql` over files, executing each with `EXECUTE`, is
orchestration that SQL cannot express as a set. Inside operations and
migrations, change rows with set-based statements (`UPDATE ... WHERE`,
`INSERT ... SELECT`), not one row at a time.
