CREATE OR REPLACE FUNCTION upsert_user(p_email TEXT, p_name TEXT DEFAULT NULL)
RETURNS "user"
LANGUAGE SQL
AS $$
    INSERT INTO "user" (email, name) VALUES (p_email, p_name)
    ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name
    RETURNING *;
$$ VOLATILE;

CREATE OR REPLACE FUNCTION get_user(p_email TEXT)
RETURNS "user"
LANGUAGE SQL
STABLE
AS $$
    SELECT * FROM "user" WHERE email = p_email;
$$;

CREATE OR REPLACE FUNCTION delete_user(p_email TEXT)
RETURNS BOOLEAN
LANGUAGE plpgsql
AS $$
BEGIN
    DELETE FROM "user" WHERE email = p_email;
    RETURN FOUND;
END;
$$;
