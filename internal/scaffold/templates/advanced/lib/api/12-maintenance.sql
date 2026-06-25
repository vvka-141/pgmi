/*
<pgmi-meta
    id="a7f02000-0004-4000-8000-000000000012"
    idempotent="true">
  <description>
    Exchange retention: BRIN indexes on enqueued_at and a batch
    purge procedure for the three exchange tables.
  </description>
  <sortKeys>
    <key>004/012</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE INDEX IF NOT EXISTS bx_rest_exchange_enqueued
    ON api.rest_exchange USING brin (enqueued_at);

CREATE INDEX IF NOT EXISTS bx_rpc_exchange_enqueued
    ON api.rpc_exchange USING brin (enqueued_at);

CREATE INDEX IF NOT EXISTS bx_mcp_exchange_enqueued
    ON api.mcp_exchange USING brin (enqueued_at);

CREATE OR REPLACE PROCEDURE api.purge_exchanges(
    retention_interval interval DEFAULT interval '30 days',
    batch_size int DEFAULT 5000,
    max_batches int DEFAULT 100
)
LANGUAGE plpgsql
AS $proc$
DECLARE
    v_cutoff timestamptz := now() - retention_interval;
    v_batch int := 0;
    v_deleted int;
    v_batch_deleted int;
    v_total int := 0;
BEGIN
    LOOP
        v_batch := v_batch + 1;
        EXIT WHEN v_batch > max_batches;

        v_batch_deleted := 0;

        -- EXTENSION POINT: archive before delete.
        --   INSERT INTO archive.rest_exchange SELECT * FROM api.rest_exchange WHERE enqueued_at < v_cutoff LIMIT batch_size;
        --   COPY (SELECT ... WHERE enqueued_at < v_cutoff LIMIT batch_size) TO PROGRAM 's3-upload';
        --   INSERT INTO foreign_table SELECT ...;  -- FDW to data warehouse

        DELETE FROM api.rest_exchange
        WHERE ctid = ANY (ARRAY(
            SELECT ctid FROM api.rest_exchange
            WHERE enqueued_at < v_cutoff
            LIMIT batch_size));
        GET DIAGNOSTICS v_deleted = ROW_COUNT;
        v_batch_deleted := v_batch_deleted + v_deleted;

        DELETE FROM api.rpc_exchange
        WHERE ctid = ANY (ARRAY(
            SELECT ctid FROM api.rpc_exchange
            WHERE enqueued_at < v_cutoff
            LIMIT batch_size));
        GET DIAGNOSTICS v_deleted = ROW_COUNT;
        v_batch_deleted := v_batch_deleted + v_deleted;

        DELETE FROM api.mcp_exchange
        WHERE ctid = ANY (ARRAY(
            SELECT ctid FROM api.mcp_exchange
            WHERE enqueued_at < v_cutoff
            LIMIT batch_size));
        GET DIAGNOSTICS v_deleted = ROW_COUNT;
        v_batch_deleted := v_batch_deleted + v_deleted;

        v_total := v_total + v_batch_deleted;

        COMMIT;
        EXIT WHEN v_batch_deleted = 0;
        RAISE NOTICE 'purge_exchanges: batch % deleted % (total %)', v_batch, v_batch_deleted, v_total;
    END LOOP;
    RAISE NOTICE 'purge_exchanges: complete - % rows across % batches', v_total, v_batch - 1;
END;
$proc$;

DO $$ BEGIN
    RAISE NOTICE '  + BRIN indexes on exchange enqueued_at';
    RAISE NOTICE '  + api.purge_exchanges() - batch retention procedure';
END $$;
