/*
<pgmi-meta
    id="0c520c79-0e09-4da8-9ebc-6771a1bf5cb8"
    idempotent="true">
  <description>
    Text utility functions: is_regexp validation for PostgreSQL regular
    expressions, like_escape for user text inside LIKE patterns
  </description>
  <sortKeys>
    <key>001/000</key>
  </sortKeys>
</pgmi-meta>
*/

-- Matching against '' compiles the pattern: a caller checks user input here
-- instead of failing on it later.
CREATE OR REPLACE FUNCTION common.is_regexp(p_pattern text)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
BEGIN
    PERFORM '' ~ p_pattern;
    RETURN TRUE;
EXCEPTION
    WHEN invalid_regular_expression THEN
        RETURN FALSE;
END;
$$;

COMMENT ON FUNCTION common.is_regexp(text) IS
'Validates if text is a valid PostgreSQL POSIX regular expression. Returns TRUE for valid patterns, FALSE for invalid.';


DO $test$
DECLARE
    v_case record;
BEGIN
    FOR v_case IN
        SELECT * FROM (VALUES
            ('^[a-z]+$', true),
            ('^(get|post|put|delete|patch)$', true),
            ('[invalid', false),
            ('^(unclosed', false),
            ('^/api/v?[0-9]+(\.|$)', true),
            ('/hello/world', true),
            ('', true),
            (NULL, NULL),
            ('\k', false)
        ) AS c(pattern, want)
    LOOP
        IF common.is_regexp(v_case.pattern) IS DISTINCT FROM v_case.want THEN
            RAISE EXCEPTION 'TEST FAILED: common.is_regexp(%) should be %', quote_nullable(v_case.pattern), v_case.want;
        END IF;
    END LOOP;
END $test$;


-- A route pattern that matches the UUID with or without its dashes.
CREATE OR REPLACE FUNCTION common.to_regexp(uuid)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
SELECT REPLACE($1::TEXT, '-', '-?');
$$;

COMMENT ON FUNCTION common.to_regexp(uuid) IS
'Converts UUID to regex pattern with optional dashes for flexible URL matching';


-- Text that differs only in case and whitespace gets one lookup key.
CREATE OR REPLACE FUNCTION common.semantic_fingerprint(content text)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT encode(
        extensions.digest(
            convert_to(
                lower(
                    regexp_replace(
                        regexp_replace(content, '^\s+|\s+$', '', 'g'),
                        '\s+', ' ', 'g')
                ), 'UTF8'
            ), 'sha256'
        ), 'hex'
    );
$$;

DO $$
BEGIN
    IF common.semantic_fingerprint('  Hello   World  ') IS DISTINCT FROM
       common.semantic_fingerprint('hello world') THEN
        RAISE EXCEPTION 'semantic_fingerprint failed: normalization';
    END IF;
END $$;

COMMENT ON FUNCTION common.semantic_fingerprint(text) IS
    'Generates SHA-256 hash for normalized text content, ensuring consistent lookup for semantic matching';

-- User text inside a LIKE pattern, as in title ILIKE '%' || like_escape(p_search) || '%'.
CREATE OR REPLACE FUNCTION common.like_escape(p_text text)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT replace(replace(replace(p_text, '\', '\\'), '%', '\%'), '_', '\_');
$$;

COMMENT ON FUNCTION common.like_escape(text) IS
'Escapes \, % and _ with a backslash so the text matches literally inside a LIKE or ILIKE pattern.';

DO $test$
BEGIN
    IF common.like_escape('50%_off\x') IS DISTINCT FROM '50\%\_off\\x' THEN
        RAISE EXCEPTION 'TEST FAILED: like_escape(''50%%_off\x'') = %', common.like_escape('50%_off\x');
    END IF;
    IF NOT ('a 50%_off\x deal' LIKE '%' || common.like_escape('50%_off\x') || '%') THEN
        RAISE EXCEPTION 'TEST FAILED: escaped text must match itself';
    END IF;
    IF 'a 50 percent off' LIKE '%' || common.like_escape('50%') || '%'
       OR 'task1' LIKE common.like_escape('task_')
       OR 'ab' LIKE common.like_escape('a\b') THEN
        RAISE EXCEPTION 'TEST FAILED: %%, _ and \ must not act as wildcards or escapes';
    END IF;
    IF common.like_escape('') IS DISTINCT FROM '' OR common.like_escape(NULL) IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: like_escape of '''' and NULL';
    END IF;
END $test$;
