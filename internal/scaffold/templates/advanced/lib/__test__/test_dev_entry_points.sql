-- Development impersonation exists only while the database is dev: a database
-- relabelled from dev loses api.open_dev_login_session.

DO $$
BEGIN
    IF current_setting('deployment.env') IS DISTINCT FROM 'dev'
       AND to_regprocedure('api.open_dev_login_session(text, text, text, text)') IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: api.open_dev_login_session survives on a % database', current_setting('deployment.env');
    END IF;
    RAISE NOTICE '  + dev entry points: impersonation exists only on dev';
END $$;
