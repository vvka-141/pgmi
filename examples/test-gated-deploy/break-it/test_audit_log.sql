DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM audit_log WHERE event = 'deploy') THEN
        RAISE EXCEPTION 'audit_log must contain a deploy event';
    END IF;
END $$;
