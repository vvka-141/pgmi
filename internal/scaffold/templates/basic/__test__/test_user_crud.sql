DO $$
DECLARE
    v_user "user";
    v_original_id INT;
    v_config JSONB;
BEGIN
    -- Verify project data is accessible (pgmi loads ALL files, not just SQL)
    SELECT content::jsonb INTO v_config
    FROM pg_temp.pgmi_source_view
    WHERE path = './project.json';

    IF v_config IS NULL THEN
        RAISE EXCEPTION 'project.json should be accessible via pgmi_source_view';
    END IF;

    IF v_config ->> 'app_name' IS NULL THEN
        RAISE EXCEPTION 'project.json should contain app_name';
    END IF;

    -- Verify CRUD operations
    v_user := get_user('alice@test.com');
    IF v_user.name != 'Alice' THEN
        RAISE EXCEPTION 'get_user failed: expected Alice, got %', v_user.name;
    END IF;

    v_user := upsert_user('dave@test.com', 'Dave');
    IF v_user.email != 'dave@test.com' THEN
        RAISE EXCEPTION 'upsert_user insert failed';
    END IF;
    v_original_id := v_user.id;

    v_user := upsert_user('dave@test.com', 'David');
    IF v_user.id != v_original_id THEN
        RAISE EXCEPTION 'upsert_user not idempotent: created new row instead of updating';
    END IF;
    IF v_user.name != 'David' THEN
        RAISE EXCEPTION 'upsert_user failed: name not updated';
    END IF;

    IF NOT delete_user('bob@test.com') THEN
        RAISE EXCEPTION 'delete_user failed';
    END IF;

    v_user := get_user('bob@test.com');
    IF v_user.id IS NOT NULL THEN
        RAISE EXCEPTION 'delete_user failed: user still exists';
    END IF;
END $$;
