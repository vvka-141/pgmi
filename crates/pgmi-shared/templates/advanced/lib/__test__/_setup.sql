-- Library test fixture: the identity issuers the tests' people sign in from, and
-- the status assertion the library tests share.

INSERT INTO membership.identity_issuer (id, kind)
VALUES ('test', 'development'), ('google', 'development'), ('github', 'development'), ('samlp', 'development')
ON CONFLICT (id) DO NOTHING;

-- Fails the test unless the response has the expected status, naming both and
-- the body; returns the body as jsonb when it is JSON.
CREATE FUNCTION pg_temp.expect_status(p_label text, p_response api.http_response, p_status int)
RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE
    v_body text := convert_from((p_response).content, 'UTF8');
BEGIN
    IF (p_response).status_code IS DISTINCT FROM p_status THEN
        RAISE EXCEPTION 'TEST FAILED: %: expected status %, got %: %', p_label, p_status, (p_response).status_code, v_body;
    END IF;
    RETURN CASE WHEN v_body IS JSON THEN v_body::jsonb END;
END $$;
