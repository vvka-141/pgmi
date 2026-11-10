# Wake up workers with LISTEN/NOTIFY, backed by a table

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
A worker must react when a row arrives: a new job, a changed setting, an
invalidated cache. Polling every second costs queries and still adds latency.
`NOTIFY` signals every session that ran `LISTEN` on a channel, but only on
commit and only to sessions connected at that moment. A listener that is down,
reconnecting or behind a pooler misses the signal, so the signal must never be
the data itself.

## Use when
- In-database or in-app processes need a low-latency "look now" signal.
- A table already holds the work or the state the signal points at.

## Don't use when
- Every change must reach the consumer even after downtime: use a
  publication and logical decoding (logical-decoding.md in this skill).
- Workers compete for the same jobs: the signal only wakes them; claim with
  `FOR UPDATE SKIP LOCKED` (skip-locked-queue.md in the postgres-concurrency
  skill).

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none. Listeners need a direct session; transaction-pooled
  connections do not keep `LISTEN` state.

## Pattern
The table is the source of truth; a trigger sends only the key:

```sql
CREATE TABLE outbox_event (
    event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic text NOT NULL,
    body jsonb NOT NULL,
    handled_at timestamptz
);

CREATE OR REPLACE FUNCTION outbox_event_signal() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('outbox_event', NEW.event_id::text);
    RETURN NULL;
END $$;

CREATE TRIGGER outbox_event_signal AFTER INSERT ON outbox_event
    FOR EACH ROW EXECUTE FUNCTION outbox_event_signal();
```

A worker runs `LISTEN outbox_event`, then on start-up and after every
reconnect processes `WHERE handled_at IS NULL` before waiting, so a missed
signal delays work but never loses it. `pg_notify(channel, payload)` is the
function form of `NOTIFY` for channel names and payloads that are not
literals.

## Proof
```sql runnable target=__test__/test_change_capture_listen_notify.sql
CREATE TEMP TABLE outbox_event (
    event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic text NOT NULL,
    body jsonb NOT NULL,
    handled_at timestamptz
);
CREATE FUNCTION pg_temp.outbox_event_signal() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('outbox_event', NEW.event_id::text);
    RETURN NULL;
END $$;
CREATE TRIGGER outbox_event_signal AFTER INSERT ON outbox_event
    FOR EACH ROW EXECUTE FUNCTION pg_temp.outbox_event_signal();

DO $$
DECLARE v_pending int;
BEGIN
    INSERT INTO outbox_event (topic, body) VALUES ('shipment', '{"id": 7}');
    SELECT count(*) INTO v_pending FROM outbox_event WHERE handled_at IS NULL;
    IF v_pending IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'expected 1 pending event, got %', v_pending;
    END IF;

    PERFORM pg_notify('outbox_event', repeat('x', 7999));
    BEGIN
        PERFORM pg_notify('outbox_event', repeat('x', 8000));
        RAISE EXCEPTION 'an 8000-byte payload was accepted';
    EXCEPTION WHEN invalid_parameter_value THEN
        NULL;
    END;
END $$;
```

This proves the trigger fires, the row is the durable record, and a payload
of 8000 bytes is refused with `22023` in the default build. It cannot prove
delivery: a test runs inside a transaction that is rolled back, and
notifications reach listeners only after a commit, in another session.

## Pitfalls
- Notifications are sent at commit, never on rollback, and a listening
  session receives them only between its own transactions. Keep listener
  transactions short.
- No durability: a listener that is disconnected at commit time never sees
  that notification. Always reconcile from the table.
- The payload must be shorter than 8000 bytes. Send a key, not the row.
- Identical channel-and-payload pairs within one transaction are folded into
  one notification; a per-row key keeps them distinct.
- The notification queue is shared; a listener stuck in a long transaction
  stops cleanup, and once the queue fills, `NOTIFY` fails at commit. Watch
  `pg_notification_queue_usage()`.
- A transaction that ran `NOTIFY` cannot be prepared for two-phase commit.

## Source
- PostgreSQL documentation: NOTIFY,
  https://www.postgresql.org/docs/current/sql-notify.html; LISTEN,
  https://www.postgresql.org/docs/current/sql-listen.html; System Information
  Functions (pg_notification_queue_usage),
  https://www.postgresql.org/docs/current/functions-info.html.
- pgmi adaptation: the outbox schema, key-only signalling with the table as
  the source of truth, and the test that proves what one transaction can show.
