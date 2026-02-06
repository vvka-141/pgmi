CREATE OR REPLACE FUNCTION upsert_user(p_email TEXT, p_name TEXT DEFAULT NULL)
RETURNS users
LANGUAGE SQL
AS $$
    INSERT INTO users (email, name) VALUES (p_email, p_name)
    ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name
    RETURNING *;
$$ VOLATILE;

CREATE OR REPLACE FUNCTION get_user(p_email TEXT)
RETURNS users
LANGUAGE SQL
STABLE
AS $$
    SELECT * FROM users WHERE email = p_email;
$$;

CREATE OR REPLACE FUNCTION delete_user(p_email TEXT)
RETURNS BOOLEAN
LANGUAGE SQL
AS $$
    DELETE FROM users WHERE email = p_email RETURNING TRUE;
$$ VOLATILE;
