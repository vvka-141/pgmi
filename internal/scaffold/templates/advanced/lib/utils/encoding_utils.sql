/*
<pgmi-meta
    id="7a8b3c45-d6e7-4f89-a0b1-c2d3e4f56789"
    idempotent="true">
  <description>
    Bytea encoding types and casts for chainable type conversion (utf8, latin1, win1252)
  </description>
  <sortKeys>
    <key>001/000</key>
  </sortKeys>
</pgmi-meta>
*/
-- ============================================================================
-- Utils: Encoding Types and Chainable Bytea Casts
-- ============================================================================
-- Provides chainable bytea decoding via PostgreSQL cast syntax:
--
--   my_bytea::utf8::jsonb    -- Decode as UTF-8, then parse as JSONB
--   my_bytea::utf8::json     -- Decode as UTF-8, then parse as JSON
--   my_bytea::utf8::xml      -- Decode as UTF-8, then parse as XML
--   my_bytea::utf8::text     -- Decode as UTF-8 to text
--   my_bytea::latin1::text   -- Decode as Latin-1 to text
--   my_bytea::win1252::text  -- Decode as Windows-1252 to text
--
-- Implementation uses composite types (not domains) because:
--   - We own composite types we create
--   - CREATE CAST requires owning at least one type
--   - Domains don't work (PostgreSQL ignores casts TO domains)
--
-- The encoding type wraps decoded text, enabling cast chaining.
-- ============================================================================

DO $$
BEGIN
    RAISE NOTICE '→ Installing encoding types and bytea casts';
END $$;

-- ============================================================================
-- Encoding Types (Composite wrappers around text)
-- ============================================================================
-- These types wrap decoded text, preserving encoding provenance.
-- As composite types we own, we can create casts from/to them.

DROP TYPE IF EXISTS utils.utf8 CASCADE;
CREATE TYPE utils.utf8 AS (v text);
COMMENT ON TYPE utils.utf8 IS
    'UTF-8 decoded text wrapper. Enables cast chaining: my_bytea::utf8::jsonb';

DROP TYPE IF EXISTS utils.latin1 CASCADE;
CREATE TYPE utils.latin1 AS (v text);
COMMENT ON TYPE utils.latin1 IS
    'Latin-1 (ISO-8859-1) decoded text wrapper. Enables cast chaining: my_bytea::latin1::text';

DROP TYPE IF EXISTS utils.win1252 CASCADE;
CREATE TYPE utils.win1252 AS (v text);
COMMENT ON TYPE utils.win1252 IS
    'Windows-1252 decoded text wrapper. Enables cast chaining: my_bytea::win1252::text';

-- ============================================================================
-- bytea → Encoding Type Casts
-- ============================================================================
-- First step in the chain: decode bytea to wrapped text.

CREATE OR REPLACE FUNCTION utils.bytea_to_utf8(input bytea)
RETURNS utils.utf8
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ROW(convert_from($1, 'UTF8'))::utils.utf8;
$$;

CREATE CAST (bytea AS utils.utf8)
    WITH FUNCTION utils.bytea_to_utf8(bytea);


CREATE OR REPLACE FUNCTION utils.bytea_to_latin1(input bytea)
RETURNS utils.latin1
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ROW(convert_from($1, 'LATIN1'))::utils.latin1;
$$;

CREATE CAST (bytea AS utils.latin1)
    WITH FUNCTION utils.bytea_to_latin1(bytea);


CREATE OR REPLACE FUNCTION utils.bytea_to_win1252(input bytea)
RETURNS utils.win1252
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ROW(convert_from($1, 'WIN1252'))::utils.win1252;
$$;

CREATE CAST (bytea AS utils.win1252)
    WITH FUNCTION utils.bytea_to_win1252(bytea);

-- ============================================================================
-- Encoding Type → text Casts
-- ============================================================================
-- Extract the decoded text from the wrapper.

CREATE OR REPLACE FUNCTION utils.utf8_to_text(input utils.utf8)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ($1).v;
$$;

CREATE CAST (utils.utf8 AS text)
    WITH FUNCTION utils.utf8_to_text(utils.utf8);


CREATE OR REPLACE FUNCTION utils.latin1_to_text(input utils.latin1)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ($1).v;
$$;

CREATE CAST (utils.latin1 AS text)
    WITH FUNCTION utils.latin1_to_text(utils.latin1);


CREATE OR REPLACE FUNCTION utils.win1252_to_text(input utils.win1252)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ($1).v;
$$;

CREATE CAST (utils.win1252 AS text)
    WITH FUNCTION utils.win1252_to_text(utils.win1252);

-- ============================================================================
-- utf8 → JSON/JSONB/XML Casts
-- ============================================================================
-- Direct conversion from utf8 wrapper to structured types.
-- These enable: my_bytea::utf8::jsonb without intermediate ::text

CREATE OR REPLACE FUNCTION utils.utf8_to_jsonb(input utils.utf8)
RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ($1).v::jsonb;
$$;

CREATE CAST (utils.utf8 AS jsonb)
    WITH FUNCTION utils.utf8_to_jsonb(utils.utf8);


CREATE OR REPLACE FUNCTION utils.utf8_to_json(input utils.utf8)
RETURNS json
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ($1).v::json;
$$;

CREATE CAST (utils.utf8 AS json)
    WITH FUNCTION utils.utf8_to_json(utils.utf8);


CREATE OR REPLACE FUNCTION utils.utf8_to_xml(input utils.utf8)
RETURNS xml
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT xmlparse(DOCUMENT ($1).v);
$$;

CREATE CAST (utils.utf8 AS xml)
    WITH FUNCTION utils.utf8_to_xml(utils.utf8);

-- ============================================================================
-- Convenience Functions (for those who prefer function syntax)
-- ============================================================================
-- Alternative to cast syntax: to_jsonb(my_bytea), utf8(my_bytea)::jsonb

CREATE OR REPLACE FUNCTION utils.utf8(input bytea)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT convert_from($1, 'UTF8');
$$;

COMMENT ON FUNCTION utils.utf8(bytea) IS
    'Decode bytea as UTF-8 text. Alternative to cast: utf8(x)::jsonb';

CREATE OR REPLACE FUNCTION utils.latin1(input bytea)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT convert_from($1, 'LATIN1');
$$;

COMMENT ON FUNCTION utils.latin1(bytea) IS
    'Decode bytea as Latin-1 text. Alternative to cast syntax.';

CREATE OR REPLACE FUNCTION utils.win1252(input bytea)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT convert_from($1, 'WIN1252');
$$;

COMMENT ON FUNCTION utils.win1252(bytea) IS
    'Decode bytea as Windows-1252 text. Alternative to cast syntax.';

CREATE OR REPLACE FUNCTION utils.to_jsonb(input bytea)
RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT convert_from($1, 'UTF8')::jsonb;
$$;

COMMENT ON FUNCTION utils.to_jsonb(bytea) IS
    'Decode bytea as UTF-8 and parse as JSONB. One-step shortcut.';

CREATE OR REPLACE FUNCTION utils.to_json(input bytea)
RETURNS json
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT convert_from($1, 'UTF8')::json;
$$;

COMMENT ON FUNCTION utils.to_json(bytea) IS
    'Decode bytea as UTF-8 and parse as JSON. One-step shortcut.';

CREATE OR REPLACE FUNCTION utils.to_xml(input bytea)
RETURNS xml
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT xmlparse(DOCUMENT convert_from($1, 'UTF8'));
$$;

COMMENT ON FUNCTION utils.to_xml(bytea) IS
    'Decode bytea as UTF-8 and parse as XML document. One-step shortcut.';

-- ============================================================================
-- Inline Tests
-- ============================================================================

DO $test$
DECLARE
    v_bytea_json bytea := convert_to('{"key": "value", "num": 42}', 'UTF8');
    v_bytea_xml bytea := convert_to('<root><item>test</item></root>', 'UTF8');
    v_bytea_latin1 bytea := convert_to('Héllo Wörld', 'LATIN1');
    v_result_text text;
    v_result_jsonb jsonb;
    v_result_json json;
    v_result_xml xml;
BEGIN
    RAISE NOTICE '→ Testing encoding types and casts';

    -- Test 1: bytea::utf8::text chain
    v_result_text := v_bytea_json::utils.utf8::text;
    IF v_result_text != '{"key": "value", "num": 42}' THEN
        RAISE EXCEPTION 'bytea::utf8::text failed: got %', v_result_text;
    END IF;
    RAISE NOTICE '  ✓ bytea::utf8::text chain works';

    -- Test 2: bytea::utf8::jsonb chain
    v_result_jsonb := v_bytea_json::utils.utf8::jsonb;
    IF v_result_jsonb->>'key' != 'value' THEN
        RAISE EXCEPTION 'bytea::utf8::jsonb failed: key extraction incorrect';
    END IF;
    IF (v_result_jsonb->>'num')::int != 42 THEN
        RAISE EXCEPTION 'bytea::utf8::jsonb failed: num extraction incorrect';
    END IF;
    RAISE NOTICE '  ✓ bytea::utf8::jsonb chain works';

    -- Test 3: bytea::utf8::json chain
    v_result_json := v_bytea_json::utils.utf8::json;
    IF v_result_json->>'key' != 'value' THEN
        RAISE EXCEPTION 'bytea::utf8::json failed: key extraction incorrect';
    END IF;
    RAISE NOTICE '  ✓ bytea::utf8::json chain works';

    -- Test 4: bytea::utf8::xml chain
    v_result_xml := v_bytea_xml::utils.utf8::xml;
    IF (xpath('/root/item/text()', v_result_xml))[1]::text != 'test' THEN
        RAISE EXCEPTION 'bytea::utf8::xml failed: xpath extraction incorrect';
    END IF;
    RAISE NOTICE '  ✓ bytea::utf8::xml chain works';

    -- Test 5: bytea::latin1::text chain
    v_result_text := v_bytea_latin1::utils.latin1::text;
    IF v_result_text != 'Héllo Wörld' THEN
        RAISE EXCEPTION 'bytea::latin1::text failed: got %', v_result_text;
    END IF;
    RAISE NOTICE '  ✓ bytea::latin1::text chain works';

    -- Test 6: Convenience function utf8()::jsonb
    v_result_jsonb := utils.utf8(v_bytea_json)::jsonb;
    IF v_result_jsonb->>'key' != 'value' THEN
        RAISE EXCEPTION 'utf8()::jsonb failed';
    END IF;
    RAISE NOTICE '  ✓ utf8() function with cast chain works';

    -- Test 7: One-step to_jsonb()
    v_result_jsonb := utils.to_jsonb(v_bytea_json);
    IF v_result_jsonb->>'key' != 'value' THEN
        RAISE EXCEPTION 'to_jsonb() failed';
    END IF;
    RAISE NOTICE '  ✓ to_jsonb() one-step function works';

    -- Test 8: NULL handling
    IF (NULL::bytea)::utils.utf8 IS NOT NULL THEN
        RAISE EXCEPTION 'NULL::bytea::utf8 should be NULL';
    END IF;
    RAISE NOTICE '  ✓ NULL handling correct';

    RAISE NOTICE '→ All encoding type and cast tests passed';
END $test$;

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    -- Grant usage on types
    EXECUTE format('GRANT USAGE ON TYPE utils.utf8 TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT USAGE ON TYPE utils.latin1 TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT USAGE ON TYPE utils.win1252 TO %I, %I', v_admin_role, v_api_role);

    -- Grant execute on all functions
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.bytea_to_utf8(bytea) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.bytea_to_latin1(bytea) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.bytea_to_win1252(bytea) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.utf8_to_text(utils.utf8) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.latin1_to_text(utils.latin1) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.win1252_to_text(utils.win1252) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.utf8_to_jsonb(utils.utf8) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.utf8_to_json(utils.utf8) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.utf8_to_xml(utils.utf8) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.utf8(bytea) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.latin1(bytea) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.win1252(bytea) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.to_jsonb(bytea) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.to_json(bytea) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION utils.to_xml(bytea) TO %I, %I', v_admin_role, v_api_role);
END $$;

DO $$
BEGIN
    RAISE NOTICE '  ✓ utils.utf8 type - UTF-8 encoding wrapper';
    RAISE NOTICE '  ✓ utils.latin1 type - Latin-1 encoding wrapper';
    RAISE NOTICE '  ✓ utils.win1252 type - Windows-1252 encoding wrapper';
    RAISE NOTICE '  ✓ bytea::utf8 cast - decode as UTF-8';
    RAISE NOTICE '  ✓ bytea::latin1 cast - decode as Latin-1';
    RAISE NOTICE '  ✓ bytea::win1252 cast - decode as Windows-1252';
    RAISE NOTICE '  ✓ utf8::text, utf8::jsonb, utf8::json, utf8::xml casts';
    RAISE NOTICE '  ✓ latin1::text, win1252::text casts';
    RAISE NOTICE '  ✓ Convenience functions: utf8(), latin1(), win1252(), to_jsonb(), to_json(), to_xml()';
END $$;
