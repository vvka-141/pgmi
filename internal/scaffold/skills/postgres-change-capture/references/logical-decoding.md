# Feed changes to an external system with a publication

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
A search index, cache or warehouse must follow a table's changes. Polling
`WHERE updated_at > $last` misses deletes and rows whose timestamps commit out
of order, and dual writes from the application drift on every failure.
PostgreSQL already writes every committed change to the WAL; a publication
names the tables to expose, and a logical replication slot streams their
changes, in commit order, to a consumer (a subscriber database or a CDC tool).

## Use when
- Another system must see every committed insert, update and delete, in
  order, including deletes.
- The consumer can hold a replication connection and acknowledge progress.

## Don't use when
- The history must be queryable inside this database: use an audit trigger
  (audit-trigger.md in this skill).
- A best-effort "something changed" wake-up is enough: use LISTEN/NOTIFY
  (listen-notify.md in this skill).

## Requires
- PostgreSQL 15+.
- Extension: none; the built-in `pgoutput` plugin serves subscribers and most
  CDC tools.
- Server settings: `wal_level = logical` (the default is `replica`; changing
  it needs a restart). Spare `max_replication_slots` and `max_wal_senders`.
  Creating a publication needs `CREATE` on the database; creating a slot needs
  the `REPLICATION` attribute.

## Pattern
The publication is schema, so it belongs in the project:

```sql
CREATE TABLE shipment (
    shipment_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    status text NOT NULL,
    carrier_note text
);

CREATE PUBLICATION shipment_feed FOR TABLE shipment (shipment_id, status);
```

The slot belongs to the consumer, which creates it once with its own
replication connection (`CREATE SUBSCRIPTION ... PUBLICATION shipment_feed`
or the CDC tool's setup), never in `deploy.sql`: a slot is not transactional
and creating one in a transaction that has written fails with `25001`.

## Proof
```sql runnable target=__test__/test_change_capture_logical_decoding.sql
CREATE TABLE pgmi_recipe_shipment (
    shipment_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    status text NOT NULL,
    carrier_note text
);
CREATE TABLE pgmi_recipe_keyless (note text);
CREATE PUBLICATION pgmi_recipe_feed
    FOR TABLE pgmi_recipe_shipment (shipment_id, status), pgmi_recipe_keyless;

DO $$
DECLARE
    v_cols name[];
BEGIN
    SELECT attnames INTO STRICT v_cols FROM pg_publication_tables
    WHERE pubname = 'pgmi_recipe_feed' AND tablename = 'pgmi_recipe_shipment';
    IF v_cols IS DISTINCT FROM ARRAY['shipment_id', 'status']::name[] THEN
        RAISE EXCEPTION 'published columns are %, expected shipment_id, status', v_cols;
    END IF;

    INSERT INTO pgmi_recipe_keyless VALUES ('x');
    BEGIN
        UPDATE pgmi_recipe_keyless SET note = 'y';
        RAISE EXCEPTION 'an UPDATE on a published table without replica identity was accepted';
    EXCEPTION WHEN object_not_in_prerequisite_state THEN
        NULL;
    END;
END $$;
```

This proves the publication's shape and the replica-identity rule it
imposes. It does not prove that changes stream: it is verified on a server with
the default `wal_level = replica`, where `CREATE PUBLICATION` succeeds with a
warning that WAL is insufficient and creating a slot fails with `55000`.
Test the slot and the consumer against a server started with
`wal_level = logical`.

## Pitfalls
- A slot retains WAL until its consumer confirms it. A consumer that stops or
  is deleted without dropping its slot fills the disk. Monitor
  `pg_replication_slots` (`active`, `wal_status`) and set
  `max_slot_wal_keep_size` so a dead consumer loses its slot, not the server.
- Once a table is in a publication that publishes updates or deletes, those
  statements fail unless the table has a replica identity: a primary key, or
  `ALTER TABLE ... REPLICA IDENTITY USING INDEX` / `FULL`.
- A column list must include the replica identity columns, and a row filter
  (`WHERE (status <> 'draft')`) may use only those columns, or published
  `UPDATE` and `DELETE` statements fail. A column list is not a security
  boundary; protect sensitive columns on the publisher.
- DDL is not replicated. Change the consumer's schema before adding a column
  the publication sends.
- Sequences and large objects are not published.

## Source
- PostgreSQL documentation: CREATE PUBLICATION,
  https://www.postgresql.org/docs/current/sql-createpublication.html; Logical
  Decoding, https://www.postgresql.org/docs/current/logicaldecoding.html;
  Write Ahead Log settings (wal_level),
  https://www.postgresql.org/docs/current/runtime-config-wal.html; Replication
  Slots, https://www.postgresql.org/docs/current/warm-standby.html.
- pgmi adaptation: the shipment schema, the split between publication (in the
  project) and slot (owned by the consumer), and the test that proves what a
  `wal_level = replica` server can show.
