DO $$
DECLARE
    v_user "user";
    v_original_id INT;
BEGIN
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
