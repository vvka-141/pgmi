# Forbid overlapping ranges per key with an exclusion constraint

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
Two bookings of the same room must not overlap in time; two price periods of
one product must not overlap. The obvious approach is a `SELECT ... WHERE
during && $1` before the insert, in application code or a trigger. Under
concurrent writes both sessions see no overlap and both insert. A `UNIQUE`
constraint cannot help: overlapping ranges are not equal values.

## Use when
- "No two rows with the same X may overlap in Y" must hold under concurrent
  writes, where Y is a time span, a numeric interval or any range.
- Only some rows take part (cancelled bookings free the slot): add a `WHERE`
  predicate to the constraint.

## Don't use when
- The rule is equality, not overlap: use `UNIQUE` or a partial unique index
  (see partial-unique-index.md in this skill).
- Overlap is allowed up to a capacity (three of five desks): that is a count,
  which no constraint expresses. Lock the parent row and count.

## Requires
- PostgreSQL 15+.
- Extension `btree_gist` (contrib, trusted, offered by the major managed
  services) for the `=` part on a scalar column such as `bigint` or `text`.
- Server settings: none.

## Pattern
```sql
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE booking (
    booking_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    room_id bigint NOT NULL,
    during tstzrange NOT NULL CHECK (NOT isempty(during)),
    status text NOT NULL DEFAULT 'confirmed'
        CHECK (status IN ('confirmed', 'cancelled')),
    CONSTRAINT booking_no_overlap EXCLUDE USING gist
        (room_id WITH =, during WITH &&) WHERE (status <> 'cancelled')
);
```

Build ranges with the default `[)` bounds, so one booking may end exactly
when the next begins.

## Proof
```sql runnable target=__test__/test_integrity_exclusion_constraint.sql
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE TEMP TABLE booking (
    booking_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    room_id bigint NOT NULL,
    during tstzrange NOT NULL CHECK (NOT isempty(during)),
    status text NOT NULL DEFAULT 'confirmed'
        CHECK (status IN ('confirmed', 'cancelled')),
    CONSTRAINT booking_no_overlap EXCLUDE USING gist
        (room_id WITH =, during WITH &&) WHERE (status <> 'cancelled')
);

DO $$
DECLARE
    v_constraint text;
BEGIN
    INSERT INTO booking (room_id, during, status) VALUES
        (1, tstzrange('2030-01-01 09:00Z', '2030-01-01 10:00Z'), 'confirmed'),
        (1, tstzrange('2030-01-01 10:00Z', '2030-01-01 11:00Z'), 'confirmed'),
        (2, tstzrange('2030-01-01 09:00Z', '2030-01-01 10:00Z'), 'confirmed'),
        (1, tstzrange('2030-01-01 09:30Z', '2030-01-01 10:30Z'), 'cancelled');

    BEGIN
        INSERT INTO booking (room_id, during)
        VALUES (1, tstzrange('2030-01-01 09:59Z', '2030-01-01 10:01Z'));
        RAISE EXCEPTION 'an overlapping booking of room 1 was accepted';
    EXCEPTION WHEN exclusion_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'booking_no_overlap' THEN
            RAISE EXCEPTION 'wrong constraint fired: %', v_constraint;
        END IF;
    END;
END $$;
```

## Pitfalls
- Without `btree_gist`, `room_id WITH =` fails with "data type bigint has no
  default operator class for access method gist". Create the extension in the
  deploy, before the table.
- A NULL in any column, or an empty range, conflicts with nothing. Declare the
  columns `NOT NULL` and reject empty ranges with a `CHECK`.
- `ON CONFLICT DO UPDATE` cannot use an exclusion constraint as its arbiter;
  `ON CONFLICT ON CONSTRAINT booking_no_overlap DO NOTHING` works but hides
  which slot was taken. Catch SQLSTATE `23P01` and report the conflict.
- Adding the constraint to a populated table builds a GiST index under an
  `ACCESS EXCLUSIVE` lock and fails on the first existing overlap; `NOT VALID`
  is not accepted. Remove overlaps in the same change, before the constraint.
- On PostgreSQL 18+, `PRIMARY KEY (room_id, during WITHOUT OVERLAPS)` declares
  the same rule as a key that temporal foreign keys can reference. It rejects
  empty ranges and has no `WHERE`, so it cannot free cancelled slots.

## Source
- PostgreSQL documentation: Exclusion Constraints,
  https://www.postgresql.org/docs/current/ddl-constraints.html; CREATE TABLE,
  EXCLUDE and WITHOUT OVERLAPS,
  https://www.postgresql.org/docs/current/sql-createtable.html; btree_gist,
  https://www.postgresql.org/docs/current/btree-gist.html.
- pgmi adaptation: the booking schema with a cancellation predicate, the test
  that proves rejection of an overlap and acceptance of adjacent, other-room
  and cancelled bookings.
