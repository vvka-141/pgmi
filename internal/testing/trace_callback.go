package testing

// TraceCallbackSQL defines a PostgreSQL function that emits NOTICE messages
// in a parseable format for test sequence verification.
//
// Format: [TRACE]event|path|directory|depth|ordinal
//
// RAISE NOTICE is non-transactional - notices are sent to the client immediately
// and survive savepoint rollbacks. This makes it ideal for verifying the complete
// test execution sequence including events that happen inside savepoints.
const TraceCallbackSQL = `
CREATE OR REPLACE FUNCTION pg_temp.trace_callback(e pg_temp.pgmi_test_event)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    RAISE NOTICE '[TRACE]%|%|%|%|%',
        e.event,
        COALESCE(e.path, ''),
        COALESCE(e.directory, ''),
        e.depth,
        e.ordinal;
END $$;
`
