SELECT pg_temp.pgmi_declare_param(
    p_key => 'admin_email',
    p_type => 'text',
    p_default_value => 'admin@example.com',
    p_description => 'Email for the initial admin user'
);

DO $$
DECLARE
    v_file RECORD;
    v_admin_email TEXT;
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    FOR v_file IN
        SELECT path FROM pg_temp.pgmi_source
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;

    v_admin_email := pg_temp.pgmi_get_param('admin_email', 'admin@example.com');
    PERFORM pg_temp.pgmi_plan_do(
        $body$BEGIN PERFORM upsert_user(%L, 'Administrator'); END;$body$,
        v_admin_email
    );

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
