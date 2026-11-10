# An outbox a job drains

A handler never calls another system: it writes an outbox row in its own
transaction, and a job sends it after commit (README "Background jobs"). The
outbox holds one organization's rows like any other table, under forced
row-level security, and a job runs outside any request, so it sees none of
them. The batch therefore calls a kernel: a function owned by the kernel role
(`pg_temp.kernel_owns`) and let through by its policy (`pg_temp.kernel_passes`).

## Contents

1. Structure (run once): `core/outbox-structure.sql`
2. The kernel and the job (re-run): `core/outbox.sql`
3. Test: `api/__test__/test_outbox.sql`

## Structure (run once)

```sql runnable target=core/outbox-structure.sql
/*
<pgmi-meta id="e7000ba7-8e9b-49d2-ab84-34f97ba5aa01" idempotent="false">
  <description>Messages a handler leaves for a job to send after commit.</description>
  <sortKeys><key>005/100/outbox/1</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.outbox_message (
    object_id       core.entity_id PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    organization_id uuid NOT NULL DEFAULT api.current_organization_id(),
    topic           text NOT NULL,
    payload         jsonb NOT NULL,
    sent_at         timestamptz
);

CREATE INDEX ix_outbox_message_pending ON core.outbox_message (organization_id) WHERE sent_at IS NULL;

SELECT pg_temp.apply_entity_table_standards('core.outbox_message');
SELECT core.apply_org_rls('core.outbox_message', p_has_created_by => false);
SELECT pg_temp.kernel_passes('core.outbox_message', '{select,update}');
```

## The kernel and the job (re-run)

```sql runnable target=core/outbox.sql
/*
<pgmi-meta id="bde70da4-db70-4cdc-9f5d-5daebef319c5" idempotent="true">
  <description>The outbox kernel, its batch function and the job.</description>
  <sortKeys><key>005/100/outbox/2</key></sortKeys>
</pgmi-meta>
*/
-- One batch across every organization: claims at most p_batch_size unsent
-- messages, sends them, and returns how many. ADAPT: the send is yours (a row
-- for a gateway to deliver, a NOTIFY); here it only marks the row sent.
CREATE OR REPLACE FUNCTION core.fn_send_outbox_batch(p_batch_size int)
RETURNS int
LANGUAGE sql
SECURITY DEFINER
SET search_path = core, common, pg_temp
AS $$
    WITH batch AS (
        SELECT object_id FROM core.outbox_message
        WHERE sent_at IS NULL
        ORDER BY created_at
        LIMIT p_batch_size
        FOR UPDATE SKIP LOCKED
    ), sent AS (
        UPDATE core.outbox_message m SET sent_at = clock_timestamp()
        FROM batch WHERE m.object_id = batch.object_id
        RETURNING 1
    )
    SELECT count(*)::int FROM sent;
$$;
SELECT pg_temp.kernel_owns('core.fn_send_outbox_batch(int)');

CREATE OR REPLACE FUNCTION api.job_send_outbox_batch(p_batch_size int)
RETURNS int LANGUAGE sql SECURITY DEFINER SET search_path = core, common, pg_temp AS $$
    SELECT core.fn_send_outbox_batch(p_batch_size);
$$;

CREATE OR REPLACE PROCEDURE api.job_send_outbox(p_batch_size int DEFAULT 5000, p_max_batches int DEFAULT NULL)
LANGUAGE plpgsql AS $$
BEGIN
    CALL api.job_run('send_outbox', 'api.job_send_outbox_batch(int)', p_batch_size, p_max_batches);
END $$;
```

A scheduler runs `CALL api.job_send_outbox();` at top level, outside any
transaction block (README "Background jobs", worker rules).

## Test

The test calls the batch function, as `api.job_run` does once per batch; the
procedure itself commits, which a deploy's transaction cannot.

```sql runnable target=api/__test__/test_outbox.sql
DO $$
DECLARE
    v_acme uuid := current_setting('test.acme_org_id')::uuid;
    v_sent int;
BEGIN
    PERFORM pg_temp.test_act_as(current_setting('test.alice_subject'), v_acme);
    INSERT INTO core.outbox_message (topic, payload)
    SELECT 'mail', jsonb_build_object('n', n) FROM generate_series(1, 3) n;

    PERFORM pg_temp.test_act_as(NULL);
    IF (SELECT count(*) FROM core.outbox_message) IS DISTINCT FROM 0::bigint THEN
        RAISE EXCEPTION 'outside a request the owner should see no organization''s messages';
    END IF;
    v_sent := api.job_send_outbox_batch(10);
    IF v_sent IS DISTINCT FROM 3 THEN
        RAISE EXCEPTION 'the outbox batch outside a request sent % of 3 messages', v_sent;
    END IF;
    IF api.job_send_outbox_batch(10) IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'a drained outbox must send nothing more';
    END IF;
END $$;
```
