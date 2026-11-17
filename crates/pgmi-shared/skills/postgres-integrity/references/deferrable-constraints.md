# Defer key checks to the end of the transaction for cycles and swaps

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
Two rules hold only once a whole change is done. Every department has a
manager and every employee belongs to a department: neither row can go in
first. Two items swap positions under `UNIQUE (playlist_id, position)`: the
first row updated collides with the second. The usual workarounds are making
a column nullable, dropping the constraint, or parking a row at a sentinel
value. Each one weakens the rule for every other writer.

## Use when
- Foreign keys form a cycle and both sides are `NOT NULL`.
- A multi-row `UPDATE` permutes values of a unique key (reorder, swap).

## Don't use when
- Rows can be inserted parent first: keep the constraint immediate, which
  reports the failing statement instead of failing at `COMMIT`.
- The constraint is `CHECK` or `NOT NULL`: they are never deferrable.
- The unique key is an `ON CONFLICT` arbiter or the target of a foreign key:
  neither accepts a deferrable constraint.

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE department (
    department_id bigint PRIMARY KEY,
    manager_id bigint NOT NULL
);
CREATE TABLE employee (
    employee_id bigint PRIMARY KEY,
    department_id bigint NOT NULL
        CONSTRAINT employee_department_fk REFERENCES department
        DEFERRABLE INITIALLY DEFERRED
);
ALTER TABLE department ADD CONSTRAINT department_manager_fk
    FOREIGN KEY (manager_id) REFERENCES employee;

CREATE TABLE playlist_item (
    playlist_id bigint NOT NULL,
    position int NOT NULL,
    CONSTRAINT playlist_item_position_key UNIQUE (playlist_id, position)
        DEFERRABLE INITIALLY IMMEDIATE
);
```

Defer one edge of a cycle, not both. `DEFERRABLE INITIALLY IMMEDIATE` on a
unique key checks at the end of each statement, which is all a swap needs;
a non-deferrable unique key checks row by row and fails on row order.

## Proof
```sql runnable target=__test__/test_integrity_deferrable_constraints.sql
CREATE TEMP TABLE department (department_id bigint PRIMARY KEY, manager_id bigint NOT NULL);
CREATE TEMP TABLE employee (
    employee_id bigint PRIMARY KEY,
    department_id bigint NOT NULL CONSTRAINT employee_department_fk
        REFERENCES department DEFERRABLE INITIALLY DEFERRED
);
ALTER TABLE department ADD CONSTRAINT department_manager_fk
    FOREIGN KEY (manager_id) REFERENCES employee;
CREATE TEMP TABLE playlist_item (
    playlist_id bigint NOT NULL,
    position int NOT NULL,
    CONSTRAINT playlist_item_position_key UNIQUE (playlist_id, position)
        DEFERRABLE INITIALLY IMMEDIATE
);

DO $$
DECLARE
    v_constraint text;
BEGIN
    INSERT INTO employee VALUES (1, 10);
    INSERT INTO department VALUES (10, 1);
    SET CONSTRAINTS employee_department_fk IMMEDIATE;
    SET CONSTRAINTS employee_department_fk DEFERRED;

    INSERT INTO employee VALUES (2, 99);
    BEGIN
        SET CONSTRAINTS employee_department_fk IMMEDIATE;
        RAISE EXCEPTION 'an employee of a missing department passed the check';
    EXCEPTION WHEN foreign_key_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'employee_department_fk' THEN
            RAISE EXCEPTION 'wrong constraint fired: %', v_constraint;
        END IF;
    END;

    INSERT INTO playlist_item VALUES (1, 1), (1, 2);
    UPDATE playlist_item SET position = 3 - position WHERE playlist_id = 1;
    IF (SELECT count(*) FROM playlist_item WHERE position IN (1, 2))
            IS DISTINCT FROM 2::bigint THEN
        RAISE EXCEPTION 'swap did not keep both positions';
    END IF;
END $$;
```

## Pitfalls
- A pgmi test never commits, so a deferred violation inside it is rolled back
  unchecked and the test passes. Run `SET CONSTRAINTS ... IMMEDIATE` before
  asserting, as the Proof does; it checks every pending row at that moment.
- In a deploy, a deferred violation surfaces at the first top-level `COMMIT`
  of `deploy.sql`, named but far from the statement that caused it. Use
  `SET CONSTRAINTS ... IMMEDIATE` right after the cyclic inserts.
- `SET CONSTRAINTS ALL DEFERRED` affects only constraints declared
  `DEFERRABLE`; everything else stays immediate.
- Every row changed under a foreign key queues a pending check in server
  memory; deferring keeps that queue until `COMMIT` instead of the end of the
  statement, so a bulk load of millions of rows under a deferred key can
  exhaust it.

## Source
- PostgreSQL documentation: CREATE TABLE, DEFERRABLE,
  https://www.postgresql.org/docs/current/sql-createtable.html; SET
  CONSTRAINTS, https://www.postgresql.org/docs/current/sql-set-constraints.html.
- pgmi adaptation: the department/employee cycle and playlist swap, the test
  that forces the deferred check before the test's rollback hides it.
