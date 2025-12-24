/*
<pgmi-meta
    id="0c520c79-0e09-4da8-9ebc-6771a1bf5cb8"
    idempotent="true">
  <description>
    Text utility functions: is_regexp validation for PostgreSQL regular expressions
  </description>
  <sortKeys>
    <key>001/000</key>
  </sortKeys>
</pgmi-meta>
*/

-- ============================================================================
-- utils.is_regexp - Validate if text is a valid PostgreSQL regular expression
-- ============================================================================
-- Tests whether the given text is a valid POSIX regular expression by
-- attempting to compile it. Returns TRUE for valid patterns, FALSE for invalid.
--
-- Example:
--   SELECT utils.is_regexp('^[a-z]+$');              -- TRUE
--   SELECT utils.is_regexp('[invalid');             -- FALSE
--   SELECT utils.is_regexp('^(get|post|put)$');     -- TRUE
--
-- Use Case:
--   Validate user-supplied regex patterns before storing or using them,
--   enabling graceful error handling instead of runtime exceptions.
-- ============================================================================

CREATE OR REPLACE FUNCTION utils.is_regexp(p_pattern text)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT           -- NULL in → NULL out
PARALLEL SAFE
AS $$
BEGIN
    -- Compile attempt: matching against an empty string triggers regex compilation
    PERFORM '' ~ p_pattern;
    RETURN TRUE;
EXCEPTION
    WHEN SQLSTATE '2201B' THEN   -- invalid_regular_expression
        RETURN FALSE;            -- Only regex-compile errors are swallowed
END;
$$;

COMMENT ON FUNCTION utils.is_regexp(text) IS
'Validates if text is a valid PostgreSQL POSIX regular expression. Returns TRUE for valid patterns, FALSE for invalid.';


-- ============================================================================
-- Inline Tests for utils.is_regexp
-- ============================================================================

DO $test$
DECLARE
    v_result boolean;
BEGIN
    RAISE NOTICE '→ Testing utils.is_regexp()';

    -- Test 1: Valid simple pattern
    v_result := utils.is_regexp('^[a-z]+$');
    IF v_result != TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: Simple valid pattern should return TRUE';
    END IF;

    -- Test 2: Valid complex pattern
    v_result := utils.is_regexp('^(get|post|put|delete|patch)$');
    IF v_result != TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: Complex valid pattern should return TRUE';
    END IF;

    -- Test 3: Invalid pattern - unclosed bracket
    v_result := utils.is_regexp('[invalid');
    IF v_result != FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: Invalid pattern (unclosed bracket) should return FALSE';
    END IF;

    -- Test 4: Invalid pattern - unclosed parenthesis
    v_result := utils.is_regexp('^(unclosed');
    IF v_result != FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: Invalid pattern (unclosed paren) should return FALSE';
    END IF;

    -- Test 5: Valid pattern with special chars
    v_result := utils.is_regexp('^/api/v?[0-9]+(\.|$)');
    IF v_result != TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: Valid pattern with special chars should return TRUE';
    END IF;

    -- Test 6: Valid literal string (no regex metacharacters)
    v_result := utils.is_regexp('/hello/world');
    IF v_result != TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: Literal string is valid regex should return TRUE';
    END IF;

    -- Test 7: Empty string (valid regex - matches empty string)
    v_result := utils.is_regexp('');
    IF v_result != TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: Empty string is valid regex should return TRUE';
    END IF;

    -- Test 8: NULL input (STRICT function returns NULL)
    v_result := utils.is_regexp(NULL);
    IF v_result IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: NULL input should return NULL (STRICT)';
    END IF;

    -- Test 9: Invalid pattern - invalid escape
    v_result := utils.is_regexp('\k'); -- \k is not a valid escape in POSIX regex
    IF v_result != FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: Invalid escape sequence should return FALSE';
    END IF;

    RAISE NOTICE '  ✓ utils.is_regexp() - all tests passed';
END $test$;


-- ============================================================================
-- utils.to_regexp - Convert UUID to regex pattern with optional dashes
-- ============================================================================
-- Converts a UUID to a regular expression pattern where dashes are optional.
-- This allows matching UUIDs in URLs with or without dash separators.
--
-- Example:
--   SELECT utils.to_regexp('550e8400-e29b-41d4-a716-446655440000'::uuid);
--   -- Returns: '550e8400-?e29b-?41d4-?a716-?446655440000'
--
--   -- This pattern matches both:
--   --   /api/550e8400-e29b-41d4-a716-446655440000/details
--   --   /api/550e8400e29b41d4a716446655440000/details
--
-- Use Case:
--   Building flexible HTTP route patterns that accept UUIDs with or without dashes.
-- ============================================================================

CREATE OR REPLACE FUNCTION utils.to_regexp(uuid)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
SELECT REPLACE($1::TEXT, '-', '-?');
$$;

COMMENT ON FUNCTION utils.to_regexp(uuid) IS
'Converts UUID to regex pattern with optional dashes for flexible URL matching';






-- ============================================================================
-- semantic_fingerprint() - Normalized Text Hash
-- ============================================================================
-- Generates SHA-256 hash for normalized text content, ensuring consistent
-- lookup for semantic matching.
--
-- Example:
--   SELECT internal.semantic_fingerprint('  Hello   World  ');
--   SELECT internal.semantic_fingerprint('hello world');
--   -- Both return same hash

CREATE OR REPLACE FUNCTION utils.semantic_fingerprint(content text)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT encode(
        sha256(
            convert_to(
                lower(
                    regexp_replace(
                        regexp_replace(content, '^\s+|\s+$', '', 'g'),
                        '\s+', ' ', 'g')
                ), 'UTF8'
            )
        ), 'hex'
    );
$$;

-- Inline test
DO $$
BEGIN
    IF utils.semantic_fingerprint('  Hello   World  ') !=
       utils.semantic_fingerprint('hello world') THEN
        RAISE EXCEPTION 'semantic_fingerprint failed: normalization';
    END IF;
END $$;

COMMENT ON FUNCTION utils.semantic_fingerprint(text) IS
    'Generates SHA-256 hash for normalized text content, ensuring consistent lookup for semantic matching';