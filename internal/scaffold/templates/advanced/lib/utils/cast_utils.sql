/*
<pgmi-meta
    id="39c75443-ec03-4212-9140-755626f56c01"
    idempotent="true">
  <description>
    Type conversion utility functions with fallback defaults (UUID, interval, timestamp, boolean, numeric)
  </description>
  <sortKeys>
    <key>001/000</key>
  </sortKeys>
</pgmi-meta>
*/
-- ============================================================================
-- Utils: Type Conversion Functions
-- ============================================================================
-- Safe type conversion with fallback defaults for UUID, interval, timestamp,
-- boolean, integer, bigint, and numeric types.
-- ============================================================================

-- Idempotency: Drop existing operators before recreating
DROP OPERATOR IF EXISTS ?| (text, uuid) CASCADE;
DROP OPERATOR IF EXISTS ?| (text, boolean) CASCADE;
DROP OPERATOR IF EXISTS ?| (text, integer) CASCADE;
DROP OPERATOR IF EXISTS ?| (text, bigint) CASCADE;
DROP OPERATOR IF EXISTS ?| (text, numeric) CASCADE;
DROP OPERATOR IF EXISTS ?| (text, interval) CASCADE;
DROP OPERATOR IF EXISTS ?| (text, timestamp) CASCADE;

DO $$
BEGIN
    RAISE NOTICE '→ Installing utils type conversion functions';
END $$;

-- ============================================================================
-- try_cast(text, uuid) - UUID Try-Cast with Default
-- ============================================================================
-- Attempts to parse text as UUID. Returns default_value on invalid format.
-- Accepts both standard format (with dashes) and compact format (without dashes).
--
-- Example:
--   SELECT utils.try_cast('550e8400-e29b-41d4-a716-446655440000', extensions.uuid_nil());
--   SELECT utils.try_cast('invalid', extensions.uuid_nil());  -- Returns nil UUID
--   SELECT '550e8400e29b41d4a716446655440000' ?| extensions.uuid_nil();  -- Operator syntax

CREATE OR REPLACE FUNCTION utils.try_cast(input text, default_value uuid)
RETURNS uuid
LANGUAGE sql
IMMUTABLE PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN $1 IS NULL THEN $2
        WHEN $1 ~* '^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$'
        THEN $1::uuid
        ELSE $2
    END;
$$;

COMMENT ON FUNCTION utils.try_cast(text, uuid) IS
    'Try to parse text as UUID, returning default value on failure. Accepts standard (with dashes) and compact (without dashes) formats.';

CREATE OPERATOR ?| (
    LEFTARG = text,
    RIGHTARG = uuid,
    PROCEDURE = utils.try_cast
);

COMMENT ON OPERATOR ?| (text, uuid) IS
    'Try-cast operator: parse left as UUID or return right as default. Example: ''text'' ?| extensions.uuid_nil()';

-- Inline tests
DO $$
BEGIN
    IF utils.try_cast('550e8400-e29b-41d4-a716-446655440000', extensions.uuid_nil()) = extensions.uuid_nil() THEN
        RAISE EXCEPTION 'try_cast(uuid) failed: standard format should parse';
    END IF;
    IF utils.try_cast('550e8400e29b41d4a716446655440000', extensions.uuid_nil()) = extensions.uuid_nil() THEN
        RAISE EXCEPTION 'try_cast(uuid) failed: compact format should parse';
    END IF;
    IF utils.try_cast('invalid', extensions.uuid_nil()) != extensions.uuid_nil() THEN
        RAISE EXCEPTION 'try_cast(uuid) failed: invalid input should return default';
    END IF;
    IF ('550e8400-e29b-41d4-a716-446655440000' ?| extensions.uuid_nil()) = extensions.uuid_nil() THEN
        RAISE EXCEPTION 'operator ?|(uuid) failed: valid UUID should parse';
    END IF;
    IF ('invalid' ?| extensions.uuid_nil()) != extensions.uuid_nil() THEN
        RAISE EXCEPTION 'operator ?|(uuid) failed: invalid input should return default';
    END IF;
END $$;

-- ============================================================================
-- uuid_is_nil() - Check for NIL UUID
-- ============================================================================
-- Returns true if UUID is nil (all zeros).
-- Uses extensions.uuid_nil() from uuid-ossp extension.
--
-- Example:
--   SELECT utils.uuid_is_nil('00000000-0000-0000-0000-000000000000'::uuid);
--   -- Returns: true

CREATE OR REPLACE FUNCTION utils.uuid_is_nil(input uuid)
RETURNS boolean
LANGUAGE sql
IMMUTABLE PARALLEL SAFE
AS $$
    SELECT $1 IS NOT NULL AND $1 = extensions.uuid_nil();
$$;

COMMENT ON FUNCTION utils.uuid_is_nil(uuid) IS
    'Check if UUID is nil (all zeros). Uses extensions.uuid_nil() from uuid-ossp extension.';

-- Inline test
DO $$
BEGIN
    IF NOT utils.uuid_is_nil(extensions.uuid_nil()) THEN
        RAISE EXCEPTION 'uuid_is_nil failed: nil UUID';
    END IF;
    IF utils.uuid_is_nil('550e8400-e29b-41d4-a716-446655440000'::uuid) THEN
        RAISE EXCEPTION 'uuid_is_nil failed: non-nil UUID';
    END IF;
END $$;

-- ============================================================================
-- try_cast(text, boolean) - Boolean Try-Cast with Default
-- ============================================================================
-- Attempts to parse text as boolean with comprehensive format support.
-- True values: 'true', 't', 'yes', 'y', 'on', '1' (case insensitive)
-- False values: 'false', 'f', 'no', 'n', 'off', '0' (case insensitive)
-- Returns default_value for any other input.
--
-- Examples:
--   SELECT utils.try_cast('true', false);    -- Returns: true
--   SELECT utils.try_cast('1', false);       -- Returns: true
--   SELECT utils.try_cast('yes', false);     -- Returns: true
--   SELECT utils.try_cast('0', true);        -- Returns: false
--   SELECT utils.try_cast('invalid', false); -- Returns: false (default)

CREATE OR REPLACE FUNCTION utils.try_cast(input text, default_value boolean)
RETURNS boolean
LANGUAGE sql
IMMUTABLE PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN $1 IS NULL THEN $2
        ELSE CASE btrim(lower($1))
            WHEN 'true'  THEN true
            WHEN 't'     THEN true
            WHEN 'yes'   THEN true
            WHEN 'y'     THEN true
            WHEN 'on'    THEN true
            WHEN '1'     THEN true
            WHEN 'false' THEN false
            WHEN 'f'     THEN false
            WHEN 'no'    THEN false
            WHEN 'n'     THEN false
            WHEN 'off'   THEN false
            WHEN '0'     THEN false
            ELSE $2
        END
    END;
$$;

COMMENT ON FUNCTION utils.try_cast(text, boolean) IS
    'Try to parse text as boolean. Accepts: true/t/yes/y/on/1 (true), false/f/no/n/off/0 (false). Case insensitive. Returns default on unrecognized input.';

CREATE OPERATOR ?| (
    LEFTARG = text,
    RIGHTARG = boolean,
    PROCEDURE = utils.try_cast
);

COMMENT ON OPERATOR ?| (text, boolean) IS
    'Try-cast operator: parse left as boolean or return right as default. Example: ''yes'' ?| false';

-- Inline tests
DO $$
BEGIN
    -- Test true values
    IF utils.try_cast('true', false) != true THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "true" should parse as true';
    END IF;
    IF utils.try_cast('TRUE', false) != true THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "TRUE" should parse as true (case insensitive)';
    END IF;
    IF utils.try_cast('t', false) != true THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "t" should parse as true';
    END IF;
    IF utils.try_cast('yes', false) != true THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "yes" should parse as true';
    END IF;
    IF utils.try_cast('Y', false) != true THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "Y" should parse as true';
    END IF;
    IF utils.try_cast('on', false) != true THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "on" should parse as true';
    END IF;
    IF utils.try_cast('1', false) != true THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "1" should parse as true';
    END IF;
    IF utils.try_cast('  1  ', false) != true THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "  1  " with whitespace should parse as true';
    END IF;

    -- Test false values
    IF utils.try_cast('false', true) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "false" should parse as false';
    END IF;
    IF utils.try_cast('FALSE', true) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "FALSE" should parse as false (case insensitive)';
    END IF;
    IF utils.try_cast('f', true) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "f" should parse as false';
    END IF;
    IF utils.try_cast('no', true) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "no" should parse as false';
    END IF;
    IF utils.try_cast('N', true) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "N" should parse as false';
    END IF;
    IF utils.try_cast('off', true) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "off" should parse as false';
    END IF;
    IF utils.try_cast('0', true) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "0" should parse as false';
    END IF;
    IF utils.try_cast('  0  ', true) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "  0  " with whitespace should parse as false';
    END IF;

    -- Test invalid input returns default
    IF utils.try_cast('invalid', false) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: invalid input should return default (false)';
    END IF;
    IF utils.try_cast('invalid', true) != true THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: invalid input should return default (true)';
    END IF;
    IF utils.try_cast('2', false) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: "2" should return default';
    END IF;
    IF utils.try_cast('', false) != false THEN
        RAISE EXCEPTION 'try_cast(boolean) failed: empty string should return default';
    END IF;

    -- Test operator syntax
    IF ('yes' ?| false) != true THEN
        RAISE EXCEPTION 'operator ?|(boolean) failed: "yes" should parse as true';
    END IF;
    IF ('no' ?| true) != false THEN
        RAISE EXCEPTION 'operator ?|(boolean) failed: "no" should parse as false';
    END IF;
    IF ('invalid' ?| false) != false THEN
        RAISE EXCEPTION 'operator ?|(boolean) failed: invalid input should return default';
    END IF;
END $$;

-- ============================================================================
-- try_cast(text, integer) - Integer Try-Cast with Default
-- ============================================================================
-- Attempts to parse text as integer (32-bit: -2,147,483,648 to 2,147,483,647).
-- Handles leading/trailing whitespace, optional sign (+/-), leading zeros.
-- Returns default_value if input is invalid or out of range.
--
-- Examples:
--   SELECT utils.try_cast('42', 0);          -- Returns: 42
--   SELECT utils.try_cast('-123', 0);        -- Returns: -123
--   SELECT utils.try_cast('+99', 0);         -- Returns: 99
--   SELECT utils.try_cast('  007  ', 0);     -- Returns: 7
--   SELECT utils.try_cast('2147483648', 0);  -- Returns: 0 (overflow)
--   SELECT utils.try_cast('12.5', 0);        -- Returns: 0 (not an integer)

CREATE OR REPLACE FUNCTION utils.try_cast(input text, default_value integer)
RETURNS integer
LANGUAGE sql
IMMUTABLE PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN $1 IS NULL THEN $2
        WHEN btrim($1) ~ '^\s*[+-]?\d+\s*$' THEN
            CASE
                WHEN btrim($1)::numeric BETWEEN -2147483648 AND 2147483647
                THEN btrim($1)::integer
                ELSE $2
            END
        ELSE $2
    END;
$$;

COMMENT ON FUNCTION utils.try_cast(text, integer) IS
    'Try to parse text as integer (32-bit). Handles signs, whitespace, leading zeros. Returns default if invalid or out of range.';

CREATE OPERATOR ?| (
    LEFTARG = text,
    RIGHTARG = integer,
    PROCEDURE = utils.try_cast
);

COMMENT ON OPERATOR ?| (text, integer) IS
    'Try-cast operator: parse left as integer or return right as default. Example: ''42'' ?| 0';

-- Inline tests
DO $$
BEGIN
    -- Test valid integers
    IF utils.try_cast('42', 0) != 42 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: "42" should parse';
    END IF;
    IF utils.try_cast('-123', 0) != -123 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: "-123" should parse';
    END IF;
    IF utils.try_cast('+99', 0) != 99 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: "+99" should parse';
    END IF;
    IF utils.try_cast('0', -1) != 0 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: "0" should parse';
    END IF;
    IF utils.try_cast('007', 0) != 7 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: "007" with leading zeros should parse as 7';
    END IF;
    IF utils.try_cast('  42  ', 0) != 42 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: "  42  " with whitespace should parse';
    END IF;

    -- Test range boundaries
    IF utils.try_cast('2147483647', 0) != 2147483647 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: max int32 should parse';
    END IF;
    IF utils.try_cast('-2147483648', 0) != -2147483648 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: min int32 should parse';
    END IF;

    -- Test overflow returns default
    IF utils.try_cast('2147483648', 0) != 0 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: overflow should return default';
    END IF;
    IF utils.try_cast('-2147483649', 0) != 0 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: underflow should return default';
    END IF;
    IF utils.try_cast('9999999999', 0) != 0 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: large overflow should return default';
    END IF;

    -- Test invalid input returns default
    IF utils.try_cast('12.5', 0) != 0 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: decimal should return default';
    END IF;
    IF utils.try_cast('1e5', 0) != 0 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: scientific notation should return default';
    END IF;
    IF utils.try_cast('invalid', 0) != 0 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: non-numeric should return default';
    END IF;
    IF utils.try_cast('', 0) != 0 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: empty string should return default';
    END IF;
    IF utils.try_cast('12abc', 0) != 0 THEN
        RAISE EXCEPTION 'try_cast(integer) failed: mixed alphanumeric should return default';
    END IF;

    -- Test operator syntax
    IF ('42' ?| 0) != 42 THEN
        RAISE EXCEPTION 'operator ?|(integer) failed: "42" should parse';
    END IF;
    IF ('invalid' ?| -1) != -1 THEN
        RAISE EXCEPTION 'operator ?|(integer) failed: invalid input should return default';
    END IF;
END $$;

-- ============================================================================
-- try_cast(text, bigint) - Bigint Try-Cast with Default
-- ============================================================================
-- Attempts to parse text as bigint (64-bit: -9,223,372,036,854,775,808 to 9,223,372,036,854,775,807).
-- Handles leading/trailing whitespace, optional sign (+/-), leading zeros.
-- Returns default_value if input is invalid or out of range.
--
-- Examples:
--   SELECT utils.try_cast('1705327800000', 0);     -- Returns: 1705327800000 (Unix ms)
--   SELECT utils.try_cast('9223372036854775807', 0); -- Returns: max bigint
--   SELECT utils.try_cast('99999999999999999999', 0); -- Returns: 0 (overflow)

CREATE OR REPLACE FUNCTION utils.try_cast(input text, default_value bigint)
RETURNS bigint
LANGUAGE sql
IMMUTABLE PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN $1 IS NULL THEN $2
        WHEN btrim($1) ~ '^\s*[+-]?\d+\s*$' THEN
            CASE
                WHEN btrim($1)::numeric BETWEEN -9223372036854775808 AND 9223372036854775807
                THEN btrim($1)::bigint
                ELSE $2
            END
        ELSE $2
    END;
$$;

COMMENT ON FUNCTION utils.try_cast(text, bigint) IS
    'Try to parse text as bigint (64-bit). Handles signs, whitespace, leading zeros. Returns default if invalid or out of range.';

CREATE OPERATOR ?| (
    LEFTARG = text,
    RIGHTARG = bigint,
    PROCEDURE = utils.try_cast
);

COMMENT ON OPERATOR ?| (text, bigint) IS
    'Try-cast operator: parse left as bigint or return right as default. Example: ''1705327800000'' ?| 0';

-- Inline tests
DO $$
BEGIN
    -- Test valid bigints
    IF utils.try_cast('42', 0::bigint) != 42 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: "42" should parse';
    END IF;
    IF utils.try_cast('-123', 0::bigint) != -123 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: "-123" should parse';
    END IF;
    IF utils.try_cast('1705327800000', 0::bigint) != 1705327800000 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: large value (Unix ms) should parse';
    END IF;
    IF utils.try_cast('  999999999999  ', 0::bigint) != 999999999999 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: value with whitespace should parse';
    END IF;

    -- Test range boundaries
    IF utils.try_cast('9223372036854775807', 0::bigint) != 9223372036854775807 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: max int64 should parse';
    END IF;
    IF utils.try_cast('-9223372036854775808', 0::bigint) != -9223372036854775808 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: min int64 should parse';
    END IF;

    -- Test overflow returns default
    IF utils.try_cast('9223372036854775808', 0::bigint) != 0 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: overflow should return default';
    END IF;
    IF utils.try_cast('-9223372036854775809', 0::bigint) != 0 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: underflow should return default';
    END IF;
    IF utils.try_cast('99999999999999999999', 0::bigint) != 0 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: large overflow should return default';
    END IF;

    -- Test invalid input returns default
    IF utils.try_cast('12.5', 0::bigint) != 0 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: decimal should return default';
    END IF;
    IF utils.try_cast('1e10', 0::bigint) != 0 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: scientific notation should return default';
    END IF;
    IF utils.try_cast('invalid', 0::bigint) != 0 THEN
        RAISE EXCEPTION 'try_cast(bigint) failed: non-numeric should return default';
    END IF;

    -- Test operator syntax
    IF ('1705327800000' ?| 0::bigint) != 1705327800000 THEN
        RAISE EXCEPTION 'operator ?|(bigint) failed: large value should parse';
    END IF;
    IF ('invalid' ?| -1::bigint) != -1 THEN
        RAISE EXCEPTION 'operator ?|(bigint) failed: invalid input should return default';
    END IF;
END $$;

-- ============================================================================
-- try_cast(text, numeric) - Numeric Try-Cast with Default
-- ============================================================================
-- Attempts to parse text as numeric (arbitrary precision decimal).
-- Handles integers, decimals, scientific notation (e.g., '1.5e10', '2E-3').
-- Supports leading/trailing whitespace, optional sign (+/-), leading dot ('.5').
-- Returns default_value if input is invalid.
--
-- Examples:
--   SELECT utils.try_cast('19.99', 0);       -- Returns: 19.99
--   SELECT utils.try_cast('.5', 0);          -- Returns: 0.5
--   SELECT utils.try_cast('1.5e10', 0);      -- Returns: 15000000000
--   SELECT utils.try_cast('2.5E-3', 0);      -- Returns: 0.0025
--   SELECT utils.try_cast('invalid', 0);     -- Returns: 0

CREATE OR REPLACE FUNCTION utils.try_cast(input text, default_value numeric)
RETURNS numeric
LANGUAGE plpgsql
IMMUTABLE PARALLEL SAFE
AS $$
BEGIN
    IF $1 IS NULL THEN
        RETURN $2;
    END IF;
    RETURN btrim($1)::numeric;
EXCEPTION
    WHEN OTHERS THEN
        RETURN $2;
END;
$$;

COMMENT ON FUNCTION utils.try_cast(text, numeric) IS
    'Try to parse text as numeric (arbitrary precision). Handles integers, decimals, scientific notation. Returns default on failure.';

CREATE OPERATOR ?| (
    LEFTARG = text,
    RIGHTARG = numeric,
    PROCEDURE = utils.try_cast
);

COMMENT ON OPERATOR ?| (text, numeric) IS
    'Try-cast operator: parse left as numeric or return right as default. Example: ''19.99'' ?| 0';

-- Inline tests
DO $$
BEGIN
    -- Test integers
    IF utils.try_cast('42', 0::numeric) != 42 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: integer "42" should parse';
    END IF;
    IF utils.try_cast('-123', 0::numeric) != -123 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: negative integer should parse';
    END IF;
    IF utils.try_cast('+99', 0::numeric) != 99 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: positive sign should parse';
    END IF;

    -- Test decimals
    IF utils.try_cast('19.99', 0::numeric) != 19.99 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: decimal "19.99" should parse';
    END IF;
    IF utils.try_cast('0.0001', 0::numeric) != 0.0001 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: small decimal should parse';
    END IF;
    IF utils.try_cast('.5', 0::numeric) != 0.5 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: leading dot ".5" should parse as 0.5';
    END IF;
    IF utils.try_cast('123.', 0::numeric) != 123 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: trailing dot "123." should parse as 123';
    END IF;
    IF utils.try_cast('  3.14  ', 0::numeric) != 3.14 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: value with whitespace should parse';
    END IF;

    -- Test scientific notation
    IF utils.try_cast('1.5e10', 0::numeric) != 15000000000 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: scientific "1.5e10" should parse';
    END IF;
    IF utils.try_cast('2.5E-3', 0::numeric) != 0.0025 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: scientific "2.5E-3" should parse';
    END IF;
    IF utils.try_cast('1e+6', 0::numeric) != 1000000 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: scientific "1e+6" should parse';
    END IF;
    IF utils.try_cast('5E2', 0::numeric) != 500 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: scientific "5E2" should parse';
    END IF;

    -- Test large precision
    IF utils.try_cast('123456789.123456789', 0::numeric) != 123456789.123456789 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: high precision value should parse';
    END IF;

    -- Test invalid input returns default
    IF utils.try_cast('invalid', 0::numeric) != 0 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: non-numeric should return default';
    END IF;
    IF utils.try_cast('', 0::numeric) != 0 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: empty string should return default';
    END IF;
    IF utils.try_cast('12.34.56', 0::numeric) != 0 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: multiple dots should return default';
    END IF;
    IF utils.try_cast('12abc', 0::numeric) != 0 THEN
        RAISE EXCEPTION 'try_cast(numeric) failed: mixed alphanumeric should return default';
    END IF;

    -- Test operator syntax
    IF ('19.99' ?| 0::numeric) != 19.99 THEN
        RAISE EXCEPTION 'operator ?|(numeric) failed: decimal should parse';
    END IF;
    IF ('1.5e10' ?| 0::numeric) != 15000000000 THEN
        RAISE EXCEPTION 'operator ?|(numeric) failed: scientific notation should parse';
    END IF;
    IF ('invalid' ?| (-1)::numeric) != -1 THEN
        RAISE EXCEPTION 'operator ?|(numeric) failed: invalid input should return default';
    END IF;
END $$;

-- ============================================================================
-- try_cast(text, interval) - Interval Try-Cast with Default
-- ============================================================================
-- Attempts to parse text as interval using multiple format strategies:
--   1. ISO-8601: P[nY][nM][nW][nD][T[nH][nM][nS]] (e.g., "P1Y2M3DT4H30M")
--   2. .NET TimeSpan: [-][d.]hh:mm[:ss[.fffffff]] (e.g., "1.12:30:45.123")
--   3. Free-form: Natural language (e.g., "2 days 3 hours", "1.5h", "3w 2d")
--
-- Returns default_value if all parsing strategies fail.
--
-- Examples:
--   SELECT utils.try_cast('P1Y2M', '0'::interval);           -- ISO-8601
--   SELECT utils.try_cast('1.12:30:45', '0'::interval);      -- TimeSpan
--   SELECT utils.try_cast('2 days 3 hours', '0'::interval);  -- Natural
--   SELECT utils.try_cast('invalid', '0'::interval);         -- Returns default

CREATE OR REPLACE FUNCTION utils.try_cast(input text, default_value interval)
RETURNS interval
LANGUAGE plpgsql
IMMUTABLE PARALLEL SAFE
AS $$
DECLARE
    v_input TEXT;
    v_result interval;
    v_match TEXT[];
BEGIN
    IF $1 IS NULL THEN
        RETURN $2;
    END IF;

    v_input := btrim($1);
    -- Use coarse patterns to classify format, then parse accordingly
    -- This avoids expensive regex validation upfront

    -- ISO-8601: starts with optional sign + 'P'
    IF v_input ~* '^-?P' THEN
        v_match := regexp_match(
            v_input,
            '^\s*(-)?P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?\s*$',
            'i'
        );
        IF v_match IS NOT NULL THEN
            RETURN (CASE WHEN v_match[1] IS NULL THEN 1 ELSE -1 END) * make_interval(
                years  => COALESCE(v_match[2]::int, 0),
                months => COALESCE(v_match[3]::int, 0),
                days   => COALESCE(v_match[5]::int, 0) + 7*COALESCE(v_match[4]::int, 0),
                hours  => COALESCE(v_match[6]::int, 0),
                mins   => COALESCE(v_match[7]::int, 0),
                secs   => COALESCE(v_match[8]::double precision, 0)
            );
        END IF;
    END IF;

    -- TimeSpan: contains digits with colon (hh:mm pattern)
    IF v_input ~ '\d+:\d+' THEN
        v_match := regexp_match(
            v_input,
            '^\s*(-)?(?:(\d+)\.)?(\d{1,2}):([0-5]\d)(?::([0-5]\d)(?:\.(\d{1,7}))?)?\s*$',
            'i'
        );
        IF v_match IS NOT NULL THEN
            RETURN (CASE WHEN v_match[1] IS NULL THEN 1 ELSE -1 END) * make_interval(
                days  => COALESCE(v_match[2]::int, 0),
                hours => COALESCE(v_match[3]::int, 0),
                mins  => COALESCE(v_match[4]::int, 0),
                secs  => COALESCE(v_match[5]::int, 0)
                        + COALESCE(('0.'||v_match[6])::double precision, 0)
            );
        END IF;
    END IF;

    -- Fallback: try PostgreSQL native interval parser
    -- Handles: '2 days', '3 hours 30 minutes', '1 week 2 days', etc.
    BEGIN
        RETURN v_input::interval;
    EXCEPTION
        WHEN OTHERS THEN
            RETURN $2;
    END;
END;
$$;

COMMENT ON FUNCTION utils.try_cast(text, interval) IS
    'Try to parse text as interval using ISO-8601, .NET TimeSpan, or natural language formats. Returns default value on failure.';

CREATE OPERATOR ?| (
    LEFTARG = text,
    RIGHTARG = interval,
    PROCEDURE = utils.try_cast
);

COMMENT ON OPERATOR ?| (text, interval) IS
    'Try-cast operator: parse left as interval or return right as default. Example: ''2 days'' ?| ''0''::interval';

-- Inline tests
DO $$
DECLARE
    v_result interval;
    v_default interval := '0'::interval;
BEGIN
    -- Test ISO-8601 format
    v_result := utils.try_cast('P1Y2M3D', v_default);
    IF v_result = v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: ISO-8601 "P1Y2M3D" should parse';
    END IF;
    IF v_result != make_interval(years => 1, months => 2, days => 3) THEN
        RAISE EXCEPTION 'try_cast(interval) failed: ISO-8601 "P1Y2M3D" incorrect result';
    END IF;

    v_result := utils.try_cast('PT5H30M', v_default);
    IF v_result = v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: ISO-8601 "PT5H30M" should parse';
    END IF;
    IF v_result != make_interval(hours => 5, mins => 30) THEN
        RAISE EXCEPTION 'try_cast(interval) failed: ISO-8601 "PT5H30M" incorrect result';
    END IF;

    v_result := utils.try_cast('P2W', v_default);
    IF v_result = v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: ISO-8601 "P2W" should parse';
    END IF;
    IF v_result != make_interval(days => 14) THEN
        RAISE EXCEPTION 'try_cast(interval) failed: ISO-8601 "P2W" incorrect result (expected 14 days)';
    END IF;

    -- Test .NET TimeSpan format
    v_result := utils.try_cast('12:30', v_default);
    IF v_result = v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: TimeSpan "12:30" should parse';
    END IF;
    IF v_result != make_interval(hours => 12, mins => 30) THEN
        RAISE EXCEPTION 'try_cast(interval) failed: TimeSpan "12:30" incorrect result';
    END IF;

    v_result := utils.try_cast('1.12:30:45', v_default);
    IF v_result = v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: TimeSpan "1.12:30:45" should parse';
    END IF;
    IF v_result != make_interval(days => 1, hours => 12, mins => 30, secs => 45) THEN
        RAISE EXCEPTION 'try_cast(interval) failed: TimeSpan "1.12:30:45" incorrect result';
    END IF;

    -- Test free-form natural language
    v_result := utils.try_cast('2 days', v_default);
    IF v_result = v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: natural "2 days" should parse';
    END IF;
    IF v_result != make_interval(days => 2) THEN
        RAISE EXCEPTION 'try_cast(interval) failed: natural "2 days" incorrect result';
    END IF;

    v_result := utils.try_cast('3 hours 30 minutes', v_default);
    IF v_result = v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: natural "3 hours 30 minutes" should parse';
    END IF;
    IF v_result != make_interval(hours => 3, mins => 30) THEN
        RAISE EXCEPTION 'try_cast(interval) failed: natural "3 hours 30 minutes" incorrect result';
    END IF;

    v_result := utils.try_cast('1w 2d 3h', v_default);
    IF v_result = v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: natural "1w 2d 3h" should parse';
    END IF;
    IF v_result != make_interval(days => 9, hours => 3) THEN
        RAISE EXCEPTION 'try_cast(interval) failed: natural "1w 2d 3h" incorrect result (expected 9 days 3 hours)';
    END IF;

    v_result := utils.try_cast('90 sec', v_default);
    IF v_result = v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: natural "90 sec" should parse';
    END IF;
    IF v_result != make_interval(secs => 90) THEN
        RAISE EXCEPTION 'try_cast(interval) failed: natural "90 sec" incorrect result';
    END IF;

    -- Test invalid input returns default
    v_result := utils.try_cast('invalid', v_default);
    IF v_result != v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: invalid input should return default';
    END IF;

    v_result := utils.try_cast('', v_default);
    IF v_result != v_default THEN
        RAISE EXCEPTION 'try_cast(interval) failed: empty input should return default';
    END IF;

    -- Test operator syntax
    v_result := '2 days 3 hours' ?| v_default;
    IF v_result = v_default THEN
        RAISE EXCEPTION 'operator ?|(interval) failed: "2 days 3 hours" should parse';
    END IF;
    IF v_result != make_interval(days => 2, hours => 3) THEN
        RAISE EXCEPTION 'operator ?|(interval) failed: "2 days 3 hours" incorrect result';
    END IF;

    v_result := 'invalid' ?| v_default;
    IF v_result != v_default THEN
        RAISE EXCEPTION 'operator ?|(interval) failed: invalid input should return default';
    END IF;
END $$;

-- ============================================================================
-- try_cast(text, timestamp) - Timestamp Try-Cast with Default
-- ============================================================================
-- Attempts to parse text as timestamp using multiple format strategies:
--   1. Unix epoch seconds: 1705327800 (integer or decimal)
--   2. Unix epoch milliseconds: 1705327800000 (JavaScript Date.now() style)
--   3. Standard formats: ISO-8601, common date formats via PostgreSQL native parser
--
-- Returns default_value if all parsing strategies fail.
--
-- Examples:
--   SELECT utils.try_cast('2025-01-15 14:30:00', CURRENT_TIMESTAMP);  -- ISO-8601
--   SELECT utils.try_cast('1705327800', CURRENT_TIMESTAMP);            -- Unix epoch seconds
--   SELECT utils.try_cast('1705327800000', CURRENT_TIMESTAMP);         -- Unix epoch milliseconds
--   SELECT utils.try_cast('invalid', CURRENT_TIMESTAMP);               -- Returns default

CREATE OR REPLACE FUNCTION utils.try_cast(input text, default_value timestamp)
RETURNS timestamp
LANGUAGE plpgsql
IMMUTABLE PARALLEL SAFE
AS $$
DECLARE
    v_input TEXT;
    v_numeric NUMERIC;
BEGIN
    IF $1 IS NULL THEN
        RETURN $2;
    END IF;

    v_input := btrim($1);

    -- Empty input returns default
    IF v_input = '' THEN
        RETURN $2;
    END IF;

    -- Unix epoch: pure numeric (integer or decimal)
    -- Pattern: optional sign, digits, optional decimal point and digits
    IF v_input ~ '^\-?\d+(\.\d+)?$' THEN
        BEGIN
            v_numeric := v_input::numeric;

            -- Distinguish milliseconds from seconds by magnitude
            -- Millisecond epoch >= 10^10 (corresponds to year ~2286 as seconds, or ~1973 as milliseconds)
            IF abs(v_numeric) >= 10000000000 THEN
                -- Millisecond epoch: divide by 1000
                RETURN to_timestamp(v_numeric / 1000.0)::timestamp;
            ELSE
                -- Second epoch (supports decimal for subsecond precision)
                -- Sanity check: PostgreSQL timestamp range is 4713 BC to 294276 AD
                -- Unix epoch range: -62135596800 (0001-01-01) to 253402300799 (9999-12-31)
                IF v_numeric BETWEEN -62135596800 AND 253402300799 THEN
                    RETURN to_timestamp(v_numeric)::timestamp;
                END IF;
            END IF;
        EXCEPTION
            WHEN OTHERS THEN
                -- Fall through to next strategy
        END;
    END IF;

    -- Fallback: try PostgreSQL native timestamp parser
    -- Handles: ISO-8601, common date formats, etc.
    -- Note: Does NOT support 'now', 'today' keywords (function is IMMUTABLE)
    BEGIN
        RETURN v_input::timestamp;
    EXCEPTION
        WHEN OTHERS THEN
            RETURN $2;
    END;
END;
$$;

COMMENT ON FUNCTION utils.try_cast(text, timestamp) IS
    'Try to parse text as timestamp using Unix epoch (seconds/milliseconds) or standard formats. Returns default value on failure.';

CREATE OPERATOR ?| (
    LEFTARG = text,
    RIGHTARG = timestamp,
    PROCEDURE = utils.try_cast
);

COMMENT ON OPERATOR ?| (text, timestamp) IS
    'Try-cast operator: parse left as timestamp or return right as default. Example: ''2025-01-15'' ?| CURRENT_TIMESTAMP';

-- Inline tests
DO $$
DECLARE
    v_result timestamp;
    v_default timestamp := '2025-01-01 00:00:00'::timestamp;
    v_expected timestamp;
BEGIN
    -- Test ISO-8601 format
    v_result := utils.try_cast('2025-01-15 14:30:00', v_default);
    v_expected := '2025-01-15 14:30:00'::timestamp;
    IF v_result != v_expected THEN
        RAISE EXCEPTION 'try_cast(timestamp) failed: ISO-8601 format should parse correctly';
    END IF;

    v_result := utils.try_cast('2025-01-15T14:30:00', v_default);
    v_expected := '2025-01-15 14:30:00'::timestamp;
    IF v_result != v_expected THEN
        RAISE EXCEPTION 'try_cast(timestamp) failed: ISO-8601 T-separator should parse correctly';
    END IF;

    -- Test date-only format
    v_result := utils.try_cast('2025-01-15', v_default);
    v_expected := '2025-01-15 00:00:00'::timestamp;
    IF v_result != v_expected THEN
        RAISE EXCEPTION 'try_cast(timestamp) failed: date-only format should parse correctly';
    END IF;

    -- Test Unix epoch seconds (integer)
    v_result := utils.try_cast('1705327800', v_default);
    v_expected := to_timestamp(1705327800)::timestamp;
    IF v_result != v_expected THEN
        RAISE EXCEPTION 'try_cast(timestamp) failed: Unix epoch seconds (integer) should parse correctly';
    END IF;

    -- Test Unix epoch seconds (decimal for subsecond precision)
    v_result := utils.try_cast('1705327800.500', v_default);
    v_expected := to_timestamp(1705327800.500)::timestamp;
    IF v_result != v_expected THEN
        RAISE EXCEPTION 'try_cast(timestamp) failed: Unix epoch seconds (decimal) should parse correctly';
    END IF;

    -- Test Unix epoch milliseconds (JavaScript Date.now() style)
    v_result := utils.try_cast('1705327800000', v_default);
    v_expected := to_timestamp(1705327800)::timestamp;
    IF v_result != v_expected THEN
        RAISE EXCEPTION 'try_cast(timestamp) failed: Unix epoch milliseconds should parse correctly';
    END IF;

    -- Test negative epoch (before 1970)
    v_result := utils.try_cast('-86400', v_default);
    v_expected := to_timestamp(-86400)::timestamp;  -- 1969-12-31
    IF v_result != v_expected THEN
        RAISE EXCEPTION 'try_cast(timestamp) failed: negative Unix epoch should parse correctly';
    END IF;

    -- Test invalid input returns default
    v_result := utils.try_cast('invalid timestamp', v_default);
    IF v_result != v_default THEN
        RAISE EXCEPTION 'try_cast(timestamp) failed: invalid input should return default';
    END IF;

    v_result := utils.try_cast('', v_default);
    IF v_result != v_default THEN
        RAISE EXCEPTION 'try_cast(timestamp) failed: empty input should return default';
    END IF;

    -- Test invalid date (out of range month)
    v_result := utils.try_cast('2025-13-01', v_default);
    IF v_result != v_default THEN
        RAISE EXCEPTION 'try_cast(timestamp) failed: invalid date should return default';
    END IF;

    -- Test operator syntax
    v_result := '2025-01-15 14:30:00' ?| v_default;
    v_expected := '2025-01-15 14:30:00'::timestamp;
    IF v_result != v_expected THEN
        RAISE EXCEPTION 'operator ?|(timestamp) failed: ISO-8601 format should parse correctly';
    END IF;

    v_result := 'invalid' ?| v_default;
    IF v_result != v_default THEN
        RAISE EXCEPTION 'operator ?|(timestamp) failed: invalid input should return default';
    END IF;
END $$;

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA utils TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA utils TO %I', v_api_role);
END $$;

DO $$
BEGIN
    RAISE NOTICE '  ✓ utils.try_cast(text, uuid) - UUID try-cast with default';
    RAISE NOTICE '  ✓ utils.try_cast(text, boolean) - boolean try-cast with default';
    RAISE NOTICE '  ✓ utils.try_cast(text, integer) - integer try-cast with default';
    RAISE NOTICE '  ✓ utils.try_cast(text, bigint) - bigint try-cast with default';
    RAISE NOTICE '  ✓ utils.try_cast(text, numeric) - numeric try-cast with default';
    RAISE NOTICE '  ✓ utils.try_cast(text, interval) - interval try-cast with default';
    RAISE NOTICE '  ✓ utils.try_cast(text, timestamp) - timestamp try-cast with default';
    RAISE NOTICE '  ✓ operator ?|(text, uuid) - UUID try-cast operator';
    RAISE NOTICE '  ✓ operator ?|(text, boolean) - boolean try-cast operator';
    RAISE NOTICE '  ✓ operator ?|(text, integer) - integer try-cast operator';
    RAISE NOTICE '  ✓ operator ?|(text, bigint) - bigint try-cast operator';
    RAISE NOTICE '  ✓ operator ?|(text, numeric) - numeric try-cast operator';
    RAISE NOTICE '  ✓ operator ?|(text, interval) - interval try-cast operator';
    RAISE NOTICE '  ✓ operator ?|(text, timestamp) - timestamp try-cast operator';
    RAISE NOTICE '  ✓ utils.uuid_is_nil(uuid) - nil UUID check';
END $$;

