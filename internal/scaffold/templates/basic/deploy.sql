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
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    )
    LOOP
        EXECUTE v_file.content;
    END LOOP;

    v_admin_email := pg_temp.pgmi_get_param('admin_email', 'admin@example.com');
    EXECUTE format('SELECT upsert_user(%L, %L)', v_admin_email, 'Administrator');
END $$;

-- Run tests
SELECT pgmi_test();
