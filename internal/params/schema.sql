-- ============================================================================
-- PGMI Session Schema (Internal Tables)
-- ============================================================================
-- Executed once per session by Go before deploy.sql runs.
-- Creates internal temp tables (prefixed with _) and helper functions.
-- Public API views are created by api-v1.sql after file loading.
--
-- DATA FLOW:
--   CLI flags ──► Go ──► _pgmi_parameter (--param key=value)
--   Project files ──► Go ──► _pgmi_source (via pgmi_register_file)
--   Test files ──► Go ──► _pgmi_test_directory + _pgmi_test_source
--   api-v1.sql ──► Creates public views (pgmi_source_view, pgmi_plan_view, etc.)
--   deploy.sql ──► queries views ──► EXECUTE content
--
-- OBJECT INDEX (search: "§" + name):
--   §_pgmi_parameter       - CLI parameters with type validation
--   §_pgmi_source          - Project files (non-test)
--   §_pgmi_source_metadata - Parsed <pgmi-meta> XML blocks
--   §_pgmi_test_directory  - Test directory hierarchy
--   §_pgmi_test_source     - Test file content
--   §pgmi_test_event       - Callback composite type
--   §pgmi_test_callback    - Default test event handler
--   §pgmi_validate_pattern - Regex validation
--   §pgmi_has_tests        - Recursive test discovery
--   §pgmi_test_plan        - Depth-first test execution order
--   §pgmi_is_sql_file      - Extension detection
--   §pgmi_register_file    - File normalization + insert
--   §pgmi_persist_test_plan - Export test plan to permanent schema
-- ============================================================================

-- Clean up (allows re-running during development)
DROP TABLE IF EXISTS pg_temp._pgmi_source CASCADE;
DROP TABLE IF EXISTS pg_temp._pgmi_parameter CASCADE;
DROP TABLE IF EXISTS pg_temp._pgmi_test_source CASCADE;
DROP TABLE IF EXISTS pg_temp._pgmi_test_directory CASCADE;


-- §_pgmi_parameter ───────────────────────────────────────────────────────────
-- Populated by: Go CLI (--param k=v)
-- Used by: deploy.sql via pgmi_parameter_view or current_setting('pgmi.key', true)
CREATE TEMP TABLE pg_temp._pgmi_parameter
(
	"key" TEXT PRIMARY KEY,
	"value" TEXT,
	"type" TEXT NOT NULL DEFAULT 'text',
	"required" BOOLEAN NOT NULL DEFAULT false,
	"default_value" TEXT,
	"description" TEXT,
	CONSTRAINT chk_key_format CHECK("key" ~ '^\w+$'),
	CONSTRAINT chk_type_valid CHECK("type" IN (
		'text', 'int', 'integer', 'bigint', 'numeric',
		'boolean', 'bool', 'uuid', 'timestamp', 'timestamptz', 'name'
	))
);

GRANT SELECT ON TABLE pg_temp._pgmi_parameter TO PUBLIC;


-- §_pgmi_source ──────────────────────────────────────────────────────────────
-- Populated by: Go via pgmi_register_file() for each discovered file
-- Used by: pgmi_plan_view, deploy.sql queries
-- Excludes: deploy.sql itself and __test__/ files (see chk_path_is_not_test)
CREATE TEMP TABLE _pgmi_source
(
    path TEXT NOT NULL PRIMARY KEY,
    name TEXT NOT NULL,
    directory TEXT NOT NULL,
    extension TEXT NOT NULL,
    depth INTEGER NOT NULL,
    content TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    checksum TEXT NOT NULL,
    pgmi_checksum TEXT NOT NULL,
    path_parts TEXT[] NOT NULL,
    is_sql_file BOOLEAN NOT NULL,
    is_test_file BOOLEAN NOT NULL,
    parent_folder_name TEXT,
    -- Path format constraints
    CONSTRAINT chk_path_format CHECK (path ~ '^\./'),
    CONSTRAINT chk_path_no_backslash CHECK (path !~ '\\'),
    CONSTRAINT chk_path_no_double_slash CHECK (path !~ '//'),
    CONSTRAINT chk_path_not_empty CHECK (length(trim(path)) > 0),
    -- Name constraints
    CONSTRAINT chk_name_format CHECK (name ~ '^[^/]+$'),
    CONSTRAINT chk_name_not_empty CHECK (name != ''),
    CONSTRAINT chk_name_no_whitespace_only CHECK (trim(name) != ''),
    -- Directory constraints
    CONSTRAINT chk_directory_format CHECK (directory ~ '^\./(?:[^/]+/)*$'),
    CONSTRAINT chk_directory_ends_slash CHECK (directory ~ '/$'),
    CONSTRAINT chk_directory_no_backslash CHECK (directory !~ '\\'),
    CONSTRAINT chk_directory_no_double_slash CHECK (directory !~ '//'),
    -- Extension constraints
    CONSTRAINT chk_extension_format CHECK (extension ~ '^(\.[a-zA-Z0-9]+)?$' OR extension = ''),
    CONSTRAINT chk_extension_no_slash CHECK (extension !~ '/'),
    -- Depth constraints
    CONSTRAINT chk_depth CHECK (depth >= 0),
    CONSTRAINT chk_depth_reasonable CHECK (depth <= 100),
    -- Size constraints
    CONSTRAINT chk_size_bytes CHECK (size_bytes >= 0),
    CONSTRAINT chk_content_not_null CHECK (content IS NOT NULL),
    CONSTRAINT chk_content_size_match CHECK (octet_length(content) = size_bytes),
    -- Checksum constraints
    CONSTRAINT chk_checksum_format CHECK (checksum ~ '^[a-fA-F0-9]{32,64}$'),
    CONSTRAINT chk_checksum_not_empty CHECK (length(trim(checksum)) > 0),
    CONSTRAINT chk_pgmi_checksum_format CHECK (pgmi_checksum ~ '^[a-fA-F0-9]{32,64}$'),
    CONSTRAINT chk_pgmi_checksum_not_empty CHECK (length(trim(pgmi_checksum)) > 0),
    -- Checksums should differ for non-empty content, but this is advisory not enforced
    -- CONSTRAINT chk_checksums_not_equal CHECK (checksum != pgmi_checksum OR content = ''),
    -- Path parts constraints
    CONSTRAINT chk_path_parts_not_empty CHECK (array_length(path_parts, 1) > 0),
    CONSTRAINT chk_depth_path_parts CHECK (array_length(path_parts, 1) = depth + 2),
    CONSTRAINT chk_path_parts_no_empty_strings CHECK (NOT ('' = ANY(path_parts))),
    CONSTRAINT chk_path_parts_first_element CHECK (path_parts[1] = '.'),
    CONSTRAINT chk_path_parts_last_element CHECK (path_parts[array_length(path_parts, 1)] = name),
    -- Parent folder constraints
    CONSTRAINT chk_parent_folder_depth CHECK (
        (depth = 0 AND parent_folder_name IS NULL) OR
        (depth > 0 AND parent_folder_name IS NOT NULL)
    ),
    CONSTRAINT chk_parent_folder_no_slash CHECK (parent_folder_name IS NULL OR parent_folder_name !~ '/'),
    -- Relational integrity constraints
    CONSTRAINT chk_path_directory_match CHECK (path = directory || name),
    CONSTRAINT chk_path_is_not_test CHECK (NOT path ~ '/__tests?__/')
);

COMMENT ON TABLE pg_temp._pgmi_source IS
    'Source files loaded by pgmi. Session-scoped (ephemeral after disconnect).';

-- Allow access from any role context
GRANT SELECT ON TABLE pg_temp._pgmi_source TO PUBLIC;


-- §_pgmi_source_metadata ──────────────────────────────────────────────────────
-- Populated by: Go parser when <pgmi-meta> XML blocks found in SQL files
-- Used by: pgmi_plan_view (LEFT JOIN for optional metadata)
-- Files without metadata: use path as fallback sort key (see plan view)
CREATE TEMP TABLE _pgmi_source_metadata (
    path TEXT PRIMARY KEY REFERENCES pg_temp._pgmi_source(path),
    id UUID NOT NULL,
    idempotent BOOLEAN NOT NULL,
    sort_keys TEXT[] NOT NULL DEFAULT '{}',
    description TEXT
);

-- GIN index for array operations on sort_keys
CREATE INDEX ix_pgmi_source_metadata_sort_keys
    ON pg_temp._pgmi_source_metadata USING GIN (sort_keys);

COMMENT ON TABLE pg_temp._pgmi_source_metadata IS
    'Parsed metadata from SQL file <pgmi-meta> blocks. Session-scoped (ephemeral).
     Each file can specify multiple sort keys for multi-phase execution.
     Files without metadata use path as fallback sort key.';

GRANT SELECT ON TABLE pg_temp._pgmi_source_metadata TO PUBLIC;

-- §_pgmi_test_directory ───────────────────────────────────────────────────────
-- Populated by: Go when discovering __test__/ directories
-- Used by: pgmi_test_plan() for depth-first traversal via parent_path
CREATE TEMP TABLE pg_temp._pgmi_test_directory
(
    path         TEXT PRIMARY KEY,
    parent_path  TEXT,
    depth        INT NOT NULL DEFAULT 0,
    CONSTRAINT chk_test_dir_path_format CHECK (path ~ '^\./' AND path ~ '/__tests?__/'),
    CONSTRAINT chk_test_dir_ends_slash CHECK (path ~ '/$')
);

CREATE INDEX ix_pgmi_test_directory_parent ON pg_temp._pgmi_test_directory(parent_path);
GRANT SELECT, INSERT ON TABLE pg_temp._pgmi_test_directory TO PUBLIC;

COMMENT ON TABLE pg_temp._pgmi_test_directory IS
    'Hierarchical test directory structure. Populated by Go, used by pgmi_test_plan() function.';

-- §_pgmi_test_source ──────────────────────────────────────────────────────────
-- Populated by: Go for files inside __test__/ directories
-- Used by: pgmi_test_plan() and pgmi_test() macro execution
-- is_fixture: true for _setup.sql files (run before tests in same directory)
CREATE TEMP TABLE pg_temp._pgmi_test_source
(
    path         TEXT NOT NULL PRIMARY KEY,
    directory    TEXT NOT NULL,
    filename     TEXT NOT NULL,
    content      TEXT NOT NULL,
    is_fixture   BOOLEAN NOT NULL DEFAULT FALSE,
    CONSTRAINT chk_test_source_path_format CHECK (path ~ '^\./'),
    CONSTRAINT fk_test_source_directory FOREIGN KEY (directory)
        REFERENCES pg_temp._pgmi_test_directory(path)
);

CREATE INDEX ix_pgmi_test_source_directory ON pg_temp._pgmi_test_source(directory);
GRANT SELECT, INSERT ON TABLE pg_temp._pgmi_test_source TO PUBLIC;

COMMENT ON TABLE pg_temp._pgmi_test_source IS
    'Test file content for pgmi_test() macro. Populated by Go from __test__/ directories.';

-- §pgmi_test_event ────────────────────────────────────────────────────────────
-- Composite type passed to test callbacks during pgmi_test() execution
-- Events: suite_start/end, fixture_start/end, test_start/end, rollback, teardown_start/end
CREATE TYPE pg_temp.pgmi_test_event AS (
    event       TEXT,       -- Event name (suite_start, test_end, rollback, etc.)
    path        TEXT,       -- Script path (NULL for suite/teardown events)
    directory   TEXT,       -- Test directory containing the script
    depth       INT,        -- Nesting level (0 = root __test__/)
    ordinal     INT,        -- Execution order (1-based, monotonically increasing)
    context     JSONB       -- Extensible payload for custom data
);

COMMENT ON TYPE pg_temp.pgmi_test_event IS
'Composite type for test lifecycle callbacks. Fields:
  event     - suite_start, fixture_start/end, test_start/end, rollback, teardown_start/end
  path      - Script path (NULL for suite/teardown events)
  directory - Test directory (e.g., ./__test__/)
  depth     - Nesting level for hierarchical test directories
  ordinal   - Execution order within the suite
  context   - JSONB for extensible custom data';

-- §pgmi_test_callback ─────────────────────────────────────────────────────────
-- Default callback: emits NOTICE for fixtures/tests, DEBUG for rollback/teardown
-- Override by passing custom function name to pgmi_test() macro
CREATE OR REPLACE FUNCTION pg_temp.pgmi_test_callback(e pg_temp.pgmi_test_event)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    CASE e.event
        WHEN 'suite_start'    THEN RAISE NOTICE '[pgmi] Test suite started';
        WHEN 'suite_end'      THEN RAISE NOTICE '[pgmi] Test suite completed (% steps)', e.ordinal;
        WHEN 'fixture_start'  THEN RAISE NOTICE '[pgmi] Fixture: %', e.path;
        WHEN 'fixture_end'    THEN NULL; -- silent
        WHEN 'test_start'     THEN RAISE NOTICE '[pgmi] Test: %', e.path;
        WHEN 'test_end'       THEN NULL; -- silent (success implicit)
        WHEN 'rollback'       THEN RAISE DEBUG '[pgmi] Rollback: %', COALESCE(e.path, e.directory);
        WHEN 'teardown_start' THEN RAISE DEBUG '[pgmi] Teardown: %', e.directory;
        WHEN 'teardown_end'   THEN NULL;
        ELSE NULL;
    END CASE;
END $$;

COMMENT ON FUNCTION pg_temp.pgmi_test_callback IS
'Default test callback that emits NOTICE/DEBUG messages for visibility.
Custom callbacks must accept (pg_temp.pgmi_test_event) and return void.
Example:
  CREATE FUNCTION pg_temp.my_observer(e pg_temp.pgmi_test_event)
  RETURNS void AS $$
  BEGIN
    INSERT INTO test_log (event, path) VALUES (e.event, e.path);
  END $$ LANGUAGE plpgsql;
  CALL pgmi_test(NULL, ''pg_temp.my_observer'');';


-- §pgmi_validate_pattern ──────────────────────────────────────────────────────
-- Validates regex syntax before use in test filtering (fail-fast on bad patterns)
CREATE OR REPLACE FUNCTION pg_temp.pgmi_validate_pattern(p_pattern TEXT)
RETURNS TEXT
LANGUAGE plpgsql STABLE
AS $$
BEGIN
    IF p_pattern IS NULL THEN
        RETURN NULL;
    END IF;

    -- Test pattern validity by attempting match against empty string
    BEGIN
        PERFORM '' ~ p_pattern;
    EXCEPTION WHEN invalid_regular_expression THEN
        RAISE EXCEPTION 'Invalid regex pattern: %', p_pattern
        USING HINT = 'Pattern must be a valid PostgreSQL POSIX regular expression';
    END;

    RETURN p_pattern;
END;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_validate_pattern IS
'Validates regex pattern syntax for test filtering.
Returns pattern unchanged if valid, raises exception with helpful message if invalid.
Prevents cryptic PostgreSQL regex errors and potential ReDoS from malformed patterns.';


-- §pgmi_has_tests ─────────────────────────────────────────────────────────────
-- Recursive check: does this directory or its children have matching tests?
-- Used by: pgmi_test_plan() to prune empty branches from traversal
CREATE OR REPLACE FUNCTION pg_temp.pgmi_has_tests(
    p_directory TEXT,
    p_pattern TEXT DEFAULT NULL
) RETURNS BOOLEAN
LANGUAGE sql STABLE
AS $$
    WITH RECURSIVE dir_tree AS (
        SELECT path
        FROM pg_temp._pgmi_test_directory
        WHERE path = p_directory
        UNION ALL
        SELECT d.path
        FROM pg_temp._pgmi_test_directory d
        INNER JOIN dir_tree dt ON d.parent_path = dt.path
    )
    SELECT EXISTS (
        SELECT 1
        FROM pg_temp._pgmi_test_source ts
        INNER JOIN dir_tree dt ON ts.directory = dt.path
        WHERE NOT ts.is_fixture
          AND (p_pattern IS NULL OR ts.path ~ pg_temp.pgmi_validate_pattern(p_pattern))
    );
$$;

COMMENT ON FUNCTION pg_temp.pgmi_has_tests IS
'Recursively checks if directory subtree contains tests matching pattern.
Returns TRUE if any non-fixture test file exists in the directory or its descendants.';


-- §pgmi_test_plan ─────────────────────────────────────────────────────────────
-- Returns depth-first execution plan: fixture → tests → recurse → teardown
-- Algorithm: ancestry arrays with sentinel chars ('' entry, '~' exit) produce DFS
-- Why COLLATE "C": ensures byte-order comparison ('~' > all paths) regardless of locale
CREATE OR REPLACE FUNCTION pg_temp.pgmi_test_plan(p_pattern TEXT DEFAULT NULL)
RETURNS TABLE (ordinal INT, step_type TEXT, script_path TEXT, directory TEXT, depth INT)
LANGUAGE sql STABLE
AS $$
WITH RECURSIVE
-- Step 1: Directories containing matching tests (via pgmi_has_tests)
relevant AS (
    SELECT d.path, d.parent_path, d.depth
    FROM pg_temp._pgmi_test_directory d
    WHERE pg_temp.pgmi_has_tests(d.path, p_pattern)
),
-- Step 2: Build tree with ancestry path for DFS ordering
tree AS (
    SELECT path, depth, ARRAY[path] AS ancestry
    FROM relevant
    WHERE parent_path IS NULL OR parent_path NOT IN (SELECT path FROM relevant)
    UNION ALL
    SELECT r.path, r.depth, t.ancestry || r.path
    FROM relevant r JOIN tree t ON r.parent_path = t.path
),
-- Step 3: Create entry/exit visits with DFS sort keys
-- Entry: append '' (empty string sorts before any path continuation)
-- Exit:  append '~' (tilde ASCII 126 sorts after all alphanumeric paths in C collation)
-- IMPORTANT: Uses COLLATE "C" to ensure byte-order comparison regardless of database collation.
-- Without "C" collation, locales like en_US.utf8 sort '~' before '.' breaking DFS order.
visits AS (
    SELECT path, depth, array_append(ancestry, ''::text) AS sort_key, false AS is_exit FROM tree
    UNION ALL
    SELECT path, depth, array_append(ancestry, '~'::text), true FROM tree
)
-- Step 4: Expand visits to fixture/test/teardown steps via LATERAL join
-- NOTE: ORDER BY uses array_to_string with COLLATE "C" because PostgreSQL arrays
-- use database collation by default, which may not be byte-ordered.
SELECT
    ROW_NUMBER() OVER (ORDER BY array_to_string(v.sort_key, E'\x01') COLLATE "C", s.sub_ord)::INT AS ordinal,
    s.step_type,
    s.script_path,
    v.path AS directory,
    v.depth
FROM visits v
CROSS JOIN LATERAL (
    -- Entry visit: fixture (sub_ord=0) then tests (sub_ord=1,2,...)
    -- Pre-order DFS: each level runs fixture+tests before descending to children
    SELECT 0 AS sub_ord, 'fixture' AS step_type, ts.path AS script_path
    FROM pg_temp._pgmi_test_source ts
    WHERE NOT v.is_exit AND ts.directory = v.path AND ts.is_fixture
    UNION ALL
    SELECT ROW_NUMBER() OVER (ORDER BY ts.filename)::INT, 'test', ts.path
    FROM pg_temp._pgmi_test_source ts
    WHERE NOT v.is_exit AND ts.directory = v.path AND NOT ts.is_fixture
      AND (p_pattern IS NULL OR ts.path ~ pg_temp.pgmi_validate_pattern(p_pattern))
    UNION ALL
    -- Exit visit: teardown only (after all children have completed)
    SELECT 0, 'teardown', NULL WHERE v.is_exit
) s
WHERE s.script_path IS NOT NULL OR s.step_type = 'teardown';
$$;

COMMENT ON FUNCTION pg_temp.pgmi_test_plan IS
'Returns pre-order depth-first test execution plan using pure SQL.
Each level: fixture → tests → children → teardown. Parent tests run before child tests.
Algorithm: ancestry arrays with sentinel chars produce DFS ordering without mutable state.
Use p_pattern to filter tests by regex pattern on script_path.';


-- §pgmi_is_sql_file ───────────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION pg_temp.pgmi_is_sql_file(filename TEXT)
RETURNS BOOLEAN
IMMUTABLE
LANGUAGE SQL
AS $$
    SELECT filename ~* E'\\.(sql|ddl|dml|dql|dcl|psql|pgsql|plpgsql)$';
$$;

-- §pgmi_register_file ─────────────────────────────────────────────────────────
-- Called by: Go for each discovered file
-- Does: normalize path (backslash→/, ensure ./), compute derived fields, INSERT
CREATE OR REPLACE FUNCTION pg_temp.pgmi_register_file(
    in_path TEXT,
    in_content TEXT,
    in_checksum TEXT,
    in_pgmi_checksum TEXT
)
RETURNS pg_temp._pgmi_source
LANGUAGE plpgsql
AS $$
DECLARE
    v_row pg_temp._pgmi_source;
    v_path TEXT;
    v_parts TEXT[];
BEGIN
    -- 🧩 1. Normalize the path
    v_path := trim(both ' ' from in_path);
    v_path := replace(v_path, E'\\', '/');                              -- convert backslashes
    IF v_path !~ '^\./' THEN
        v_path := './' || regexp_replace(v_path, '^(\./|/)*', '');      -- ensure leading ./
    END IF;
    v_path := regexp_replace(v_path, '/{2,}', '/', 'g');                -- collapse duplicate slashes

    -- 🧩 2. Compute components
    v_parts := string_to_array(v_path, '/');

    v_row.path               := v_path;
    v_row.path_parts         := v_parts;
    v_row.depth              := array_length(v_parts, 1) - 2;  -- Subtract 2: one for '.' and one for filename
    v_row.name               := v_parts[array_length(v_parts, 1)];
    v_row.directory          :=
        CASE WHEN v_row.depth > 0
             THEN array_to_string(v_parts[1:v_row.depth + 1], '/') || '/'
             ELSE './'
        END;
    v_row.extension          :=
        CASE WHEN v_row.name ~ '\.'
             THEN substring(v_row.name from '(\.[^.]+)$')
             ELSE ''
        END;
    v_row.content            := in_content;
    v_row.size_bytes         := octet_length(in_content);
    v_row.checksum           := in_checksum;
    v_row.pgmi_checksum      := in_pgmi_checksum;
    v_row.is_sql_file        := pg_temp.pgmi_is_sql_file(v_row.path);
    v_row.is_test_file       := v_row.path ~ '(^|/)__tests?__/';
    v_row.parent_folder_name :=
        CASE WHEN v_row.depth > 0
             THEN v_parts[array_length(v_parts, 1) - 1]
             ELSE NULL
        END;

    -- 🧩 3. Insert & return the row
    INSERT INTO pg_temp._pgmi_source VALUES (
        v_row.path, v_row.name, v_row.directory, v_row.extension,
        v_row.depth, v_row.content, v_row.size_bytes,
        v_row.checksum, v_row.pgmi_checksum,
        v_row.path_parts, v_row.is_sql_file, v_row.is_test_file, v_row.parent_folder_name
    )
    RETURNING * INTO v_row;

    RETURN v_row;
END;
$$;

-- §pgmi_persist_test_plan ─────────────────────────────────────────────────────
-- Exports test plan to permanent table for running tests outside pgmi session
CREATE OR REPLACE FUNCTION pg_temp.pgmi_persist_test_plan(target_schema TEXT, p_pattern TEXT DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    EXECUTE format('CREATE TABLE IF NOT EXISTS %I.pgmi_test_plan (
        ordinal INT PRIMARY KEY,
        step_type TEXT NOT NULL,
        script_path TEXT,
        directory TEXT NOT NULL,
        depth INT NOT NULL
    )', target_schema);
    EXECUTE format('TRUNCATE %I.pgmi_test_plan', target_schema);
    EXECUTE format('INSERT INTO %I.pgmi_test_plan SELECT * FROM pg_temp.pgmi_test_plan($1)', target_schema)
    USING p_pattern;
END;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_persist_test_plan IS
'Persists pgmi_test_plan() results to a permanent schema for running tests without pgmi.';
