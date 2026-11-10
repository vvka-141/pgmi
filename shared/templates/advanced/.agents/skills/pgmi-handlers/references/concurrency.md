# Concurrency in handlers

A request is one transaction, under `read committed` unless the route
declares `minTransactionIsolation`. Single-session tests never show the bugs
below; reason about two requests at once.

## Contents

1. A probe is not a lock
2. Lost updates: guard by the version the client read
3. Routes that can be retried

## 1. A probe is not a lock

`IF NOT EXISTS (...) THEN ... END IF;` followed by the write is a check and
a write in two statements. Two requests can both pass the check. Put the
precondition in the write itself, and treat a NULL result (the `UPDATE`
matched no row) as the refusal:

```sql
-- kernel: the guard predicate is part of the UPDATE
CREATE OR REPLACE FUNCTION core.fn_close_task(p_id uuid)
RETURNS core.task LANGUAGE sql AS $$
    UPDATE core.task SET status = 'done', closed_at = now()
    WHERE object_id = p_id AND status = 'in_progress'
    RETURNING *;
$$;

-- handler: probe for the clean answer, then trust the kernel's result
IF core.fn_task_problem(v_id, 'close') = 'core.task.not_found' THEN
    RETURN api.problem_response(404, 'Not Found', 'Task not found', code => 'core.task.not_found');
END IF;
v_row := core.fn_close_task(v_id);
IF v_row.object_id IS NULL THEN
    RETURN api.problem_response(409, 'Conflict', 'The task is not in progress', code => 'core.task.not_in_progress');
END IF;
```

For uniqueness, let a unique constraint decide: `INSERT ... ON CONFLICT DO
NOTHING RETURNING *` returns no row when the value is taken, which the
handler answers with 422 naming the field (reason `duplicate`).

## 2. Lost updates

Two clients read the same row and both write: one write vanishes. Guard the
write by the version the client read, and refuse when it moved:

```sql
UPDATE core.task SET title = p_title, updated_at = now()
WHERE object_id = p_id AND updated_at = p_expected_updated_at
RETURNING *;
-- no row: the row changed (or went) since the client read it
RETURN api.problem_response(409, 'Conflict', 'The task changed since you read it; reload and retry',
                            code => 'core.task.changed');
```

The version travels as `ETag` on the read and `If-Match` on the write
(`api.header((request).headers, 'if-match')`), or as a body field such as `updatedAt`.

## 3. Routes that can be retried

Under `repeatable read` or `serializable`, PostgreSQL aborts a conflicting
transaction with `40001`; a deadlock is `40P01` at any level. The dispatcher
lets both through with their SQLSTATE, and the HTTP gateway (`gateways/`) runs the whole
transaction again from a fresh snapshot.

- **Everything the handler does happens again on a retry**, except what the
  transaction rolled back. An outbound call, a message published outside the
  database, any external write is doubled. Write an outbox row instead and
  let a worker send it after commit.
- **Never retry inside PL/pgSQL.** A savepoint rolls back the writes but not
  the snapshot, so the retry reads the same data and conflicts again. Only a
  new transaction gets a new snapshot, and only the HTTP gateway (`gateways/`), which
  issued the `BEGIN`, can start one.
- **Never catch `40001`.** The failed statement rolls back to the handler's
  savepoint, the transaction commits, and the caller is told it worked.
