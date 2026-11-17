/*
<pgmi-meta
    id="7a8b3c45-d6e7-4f89-a0b1-c2d3e4f56789"
    idempotent="true">
  <description>
    Bytea encoding types and casts for chainable type conversion (utf8, latin1, win1252)
  </description>
  <sortKeys>
    <key>001/001</key>
  </sortKeys>
</pgmi-meta>
*/

-- Chainable bytea decoding through casts: my_bytea::utf8::jsonb, ::utf8::json,
-- ::utf8::xml, ::utf8::text, ::latin1::text, ::win1252::text. The wrapper
-- types are structure and live in encoding-structure.sql, which runs once.
--
-- Volatility: the convert_from-based functions are deliberately IMMUTABLE even
-- though pg_proc marks convert_from STABLE (its result depends on the database
-- encoding). The server encoding is fixed for a database's lifetime, so within
-- any one database these are deterministic — and IMMUTABLE keeps expressions
-- like to_jsonb(payload) index-eligible. XML parsing uses explicit
-- xmlparse(DOCUMENT ...), not the xmloption-dependent text::xml cast, so it is
-- deterministic as well.

-- ############################################################################
-- CASTS - bytea to a wrapper type, then the wrapper to text, jsonb, json or xml
-- ############################################################################
-- CREATE CAST has no OR REPLACE, so each cast is dropped by name before being
-- recreated. Naming them individually is what keeps the redeploy from touching
-- anything the template does not own.
DROP CAST IF EXISTS (bytea AS common.utf8);
DROP CAST IF EXISTS (bytea AS common.latin1);
DROP CAST IF EXISTS (bytea AS common.win1252);
DROP CAST IF EXISTS (common.utf8 AS text);
DROP CAST IF EXISTS (common.latin1 AS text);
DROP CAST IF EXISTS (common.win1252 AS text);
DROP CAST IF EXISTS (common.utf8 AS jsonb);
DROP CAST IF EXISTS (common.utf8 AS json);
DROP CAST IF EXISTS (common.utf8 AS xml);

-- The first step of a chain: decode bytea to wrapped text.
CREATE OR REPLACE FUNCTION common.bytea_to_utf8(input bytea)
RETURNS common.utf8
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ROW(convert_from($1, 'UTF8'))::common.utf8;
$$;

CREATE CAST (bytea AS common.utf8)
    WITH FUNCTION common.bytea_to_utf8(bytea);


CREATE OR REPLACE FUNCTION common.bytea_to_latin1(input bytea)
RETURNS common.latin1
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ROW(convert_from($1, 'LATIN1'))::common.latin1;
$$;

CREATE CAST (bytea AS common.latin1)
    WITH FUNCTION common.bytea_to_latin1(bytea);


CREATE OR REPLACE FUNCTION common.bytea_to_win1252(input bytea)
RETURNS common.win1252
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ROW(convert_from($1, 'WIN1252'))::common.win1252;
$$;

CREATE CAST (bytea AS common.win1252)
    WITH FUNCTION common.bytea_to_win1252(bytea);


-- The wrapper back to its decoded text.
CREATE OR REPLACE FUNCTION common.utf8_to_text(input common.utf8)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ($1).v;
$$;

CREATE CAST (common.utf8 AS text)
    WITH FUNCTION common.utf8_to_text(common.utf8);


CREATE OR REPLACE FUNCTION common.latin1_to_text(input common.latin1)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ($1).v;
$$;

CREATE CAST (common.latin1 AS text)
    WITH FUNCTION common.latin1_to_text(common.latin1);


CREATE OR REPLACE FUNCTION common.win1252_to_text(input common.win1252)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ($1).v;
$$;

CREATE CAST (common.win1252 AS text)
    WITH FUNCTION common.win1252_to_text(common.win1252);

-- Straight to jsonb, json or xml: my_bytea::utf8::jsonb needs no ::text between.
CREATE OR REPLACE FUNCTION common.utf8_to_jsonb(input common.utf8)
RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ($1).v::jsonb;
$$;

CREATE CAST (common.utf8 AS jsonb)
    WITH FUNCTION common.utf8_to_jsonb(common.utf8);


CREATE OR REPLACE FUNCTION common.utf8_to_json(input common.utf8)
RETURNS json
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT ($1).v::json;
$$;

CREATE CAST (common.utf8 AS json)
    WITH FUNCTION common.utf8_to_json(common.utf8);


CREATE OR REPLACE FUNCTION common.utf8_to_xml(input common.utf8)
RETURNS xml
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT xmlparse(DOCUMENT ($1).v);
$$;

CREATE CAST (common.utf8 AS xml)
    WITH FUNCTION common.utf8_to_xml(common.utf8);

-- ############################################################################
-- FUNCTIONS - the same decodings in function syntax: utf8(x)::jsonb, to_jsonb(x)
-- ############################################################################
CREATE OR REPLACE FUNCTION common.utf8(input bytea)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT convert_from($1, 'UTF8');
$$;

COMMENT ON FUNCTION common.utf8(bytea) IS
    'Decode bytea as UTF-8 text. Alternative to cast: utf8(x)::jsonb';

CREATE OR REPLACE FUNCTION common.latin1(input bytea)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT convert_from($1, 'LATIN1');
$$;

COMMENT ON FUNCTION common.latin1(bytea) IS
    'Decode bytea as Latin-1 text. Alternative to cast syntax.';

CREATE OR REPLACE FUNCTION common.win1252(input bytea)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT convert_from($1, 'WIN1252');
$$;

COMMENT ON FUNCTION common.win1252(bytea) IS
    'Decode bytea as Windows-1252 text. Alternative to cast syntax.';

CREATE OR REPLACE FUNCTION common.to_jsonb(input bytea)
RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT convert_from($1, 'UTF8')::jsonb;
$$;

COMMENT ON FUNCTION common.to_jsonb(bytea) IS
    'Decode bytea as UTF-8 and parse as JSONB. One-step shortcut.';

CREATE OR REPLACE FUNCTION common.to_json(input bytea)
RETURNS json
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT convert_from($1, 'UTF8')::json;
$$;

COMMENT ON FUNCTION common.to_json(bytea) IS
    'Decode bytea as UTF-8 and parse as JSON. One-step shortcut.';

CREATE OR REPLACE FUNCTION common.to_xml(input bytea)
RETURNS xml
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT xmlparse(DOCUMENT convert_from($1, 'UTF8'));
$$;

COMMENT ON FUNCTION common.to_xml(bytea) IS
    'Decode bytea as UTF-8 and parse as XML document. One-step shortcut.';

-- ############################################################################
-- CHECKS AND GRANTS - the chains decode as documented; roles may name the types
-- ############################################################################
DO $test$
DECLARE
    v_json bytea := convert_to('{"key": "value", "num": 42}', 'UTF8');
    v_case record;
BEGIN
    FOR v_case IN
        SELECT * FROM (VALUES
            ('bytea::utf8::text', (v_json::common.utf8::text), '{"key": "value", "num": 42}'),
            ('bytea::utf8::jsonb', ((v_json::common.utf8::jsonb)->>'key') || ((v_json::common.utf8::jsonb)->>'num'), 'value42'),
            ('bytea::utf8::json', (v_json::common.utf8::json)->>'key', 'value'),
            ('bytea::utf8::xml', (xpath('/root/item/text()', convert_to('<root><item>test</item></root>', 'UTF8')::common.utf8::xml))[1]::text, 'test'),
            ('bytea::latin1::text', convert_to('Héllo Wörld', 'LATIN1')::common.latin1::text, 'Héllo Wörld'),
            ('utf8()::jsonb', common.utf8(v_json)::jsonb->>'key', 'value'),
            ('to_jsonb()', common.to_jsonb(v_json)->>'key', 'value'),
            ('NULL::bytea::utf8', (NULL::bytea)::common.utf8::text, NULL)
        ) AS c(name, got, want)
    LOOP
        IF v_case.got IS DISTINCT FROM v_case.want THEN
            RAISE EXCEPTION '% failed: got %, want %', v_case.name, v_case.got, v_case.want;
        END IF;
    END LOOP;
END $test$;

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT USAGE ON TYPE common.utf8, common.latin1, common.win1252 TO %I, %I', v_admin_role, v_api_role);
END $$;
