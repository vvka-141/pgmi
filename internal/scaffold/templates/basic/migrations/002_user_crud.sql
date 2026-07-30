CREATE OR REPLACE FUNCTION upsert_user(p_email TEXT, p_name TEXT DEFAULT NULL)
RETURNS "user"
LANGUAGE SQL
AS $$
    INSERT INTO "user" (email, name) VALUES (p_email, p_name)
    ON CONFLICT (email) DO UPDATE SET name = COALESCE(EXCLUDED.name, "user".name)
    RETURNING *;
$$;

COMMENT ON FUNCTION upsert_user(TEXT, TEXT) IS
    'Inserts or updates a user by email. Idempotent — safe to call repeatedly with the same email. Omitting p_name leaves an existing name alone: a bare EXCLUDED.name would overwrite it with NULL, so ensuring a user exists would silently erase their name.';

CREATE OR REPLACE FUNCTION get_user(p_email TEXT)
RETURNS "user"
LANGUAGE SQL
STABLE
AS $$
    SELECT * FROM "user" WHERE email = p_email;
$$;

COMMENT ON FUNCTION get_user(TEXT) IS
    'Looks up a user by email. Returns NULL (empty row) if not found.';

CREATE OR REPLACE FUNCTION delete_user(p_email TEXT)
RETURNS BOOLEAN
LANGUAGE SQL
AS $$
    WITH deleted AS (
        DELETE FROM "user" WHERE email = p_email RETURNING 1
    )
    SELECT EXISTS (SELECT 1 FROM deleted);
$$;

COMMENT ON FUNCTION delete_user(TEXT) IS
    'Deletes a user by email. Returns true if a row was removed, false if no matching user existed.';
