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

    -- Verify CRUD operations.
    -- IS DISTINCT FROM, not !=: get_user returns a NULL row when not found, and
    -- NULL != 'Alice' is NULL, so != would let a missing fixture pass silently.
    v_user := get_user('alice@test.com');
    IF v_user.name IS DISTINCT FROM 'Alice' THEN
        RAISE EXCEPTION 'get_user failed: expected Alice, got %', v_user.name;
    END IF;

    v_user := upsert_user('dave@test.com', 'Dave');
    IF v_user.email IS DISTINCT FROM 'dave@test.com' THEN
        RAISE EXCEPTION 'upsert_user insert failed';
    END IF;
    v_original_id := v_user.id;

    v_user := upsert_user('dave@test.com', 'David');
    IF v_user.id IS DISTINCT FROM v_original_id THEN
        RAISE EXCEPTION 'upsert_user not idempotent: created new row instead of updating';
    END IF;
    IF v_user.name IS DISTINCT FROM 'David' THEN
        RAISE EXCEPTION 'upsert_user failed: name not updated';
    END IF;

    IF NOT delete_user('bob@test.com') THEN
        RAISE EXCEPTION 'delete_user failed';
    END IF;

    v_user := get_user('bob@test.com');
    IF v_user.id IS NOT NULL THEN
        RAISE EXCEPTION 'delete_user failed: user still exists';
    END IF;

    -- Edge cases: delete non-existent user returns false
    IF delete_user('nonexistent@test.com') THEN
        RAISE EXCEPTION 'delete_user should return false for non-existent user';
    END IF;

    -- Edge cases: get non-existent user returns NULL row
    v_user := get_user('nobody@test.com');
    IF v_user.id IS NOT NULL THEN
        RAISE EXCEPTION 'get_user should return NULL row for non-existent email';
    END IF;

    -- Edge cases: upsert with NULL name
    v_user := upsert_user('nullname@test.com');
    IF v_user.email IS DISTINCT FROM 'nullname@test.com' THEN
        RAISE EXCEPTION 'upsert_user with NULL name failed: wrong email';
    END IF;
    IF v_user.name IS NOT NULL THEN
        RAISE EXCEPTION 'upsert_user with NULL name should have NULL name, got %', v_user.name;
    END IF;

    -- Omitting the name on an EXISTING row must not erase it. With a bare
    -- EXCLUDED.name, "ensure this user exists" wipes the stored name.
    v_user := upsert_user('charlie@test.com');
    IF v_user.name IS DISTINCT FROM 'Charlie' THEN
        RAISE EXCEPTION 'upsert_user without a name erased the existing one: expected Charlie, got %', v_user.name;
    END IF;

    -- ...but passing one still updates it.
    v_user := upsert_user('charlie@test.com', 'Chuck');
    IF v_user.name IS DISTINCT FROM 'Chuck' THEN
        RAISE EXCEPTION 'upsert_user with a name should update it: expected Chuck, got %', v_user.name;
    END IF;
END $$;
