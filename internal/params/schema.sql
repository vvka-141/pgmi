-- Clean up
DROP TABLE IF EXISTS pg_temp.pgmi_source CASCADE;
DROP TABLE IF EXISTS pg_temp.pgmi_parameter CASCADE;
DROP TABLE IF EXISTS pg_temp.pgmi_plan CASCADE;

-- Merged parameters table: CLI loader inserts with value, pgmi_declare_param enriches with metadata
CREATE TEMP TABLE pg_temp.pgmi_parameter
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

GRANT SELECT ON TABLE pg_temp.pgmi_parameter TO PUBLIC;

-- 2️⃣ Table definition
CREATE TEMP TABLE pgmi_source
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
    CONSTRAINT chk_path_directory_match CHECK (path = directory || name)
);

-- Allow access from any role context
GRANT SELECT ON TABLE pg_temp.pgmi_source TO PUBLIC;


-- ============================================================================
-- Script Metadata Table (Session-Scoped)
-- ============================================================================
-- Stores parsed metadata from <pgmi:meta> XML blocks in SQL files.
-- Only populated for files that have metadata; files without metadata
-- use deterministic fallback identity in the plan view.
--
-- Namespace UUID for fallback identity generation: e8c72c3e-7b4a-5f9d-b8e1-4c6d8a2e5f7c
-- (derived from "pgmi.com/file-identity/v1" using UUID v5 with URL namespace)

CREATE TEMP TABLE pgmi_source_metadata (
    path TEXT PRIMARY KEY REFERENCES pg_temp.pgmi_source(path),
    id UUID NOT NULL,
    idempotent BOOLEAN NOT NULL,
    sort_keys TEXT[] NOT NULL DEFAULT '{}',
    description TEXT
);

-- GIN index for array operations on sort_keys
CREATE INDEX ix_pgmi_source_metadata_sort_keys
    ON pg_temp.pgmi_source_metadata USING GIN (sort_keys);

COMMENT ON TABLE pg_temp.pgmi_source_metadata IS
    'Parsed metadata from SQL file <pgmi-meta> blocks. Session-scoped (ephemeral).
     Each file can specify multiple sort keys for multi-phase execution.
     Files without metadata use path as fallback sort key.';

-- Allow access from any role context
GRANT SELECT ON TABLE pg_temp.pgmi_source_metadata TO PUBLIC;







-- ============================================================================
-- Execution Plan View (Multi-Phase Execution via Sort Keys)
-- ============================================================================
-- Joins pgmi_source with pgmi_source_metadata (LEFT JOIN), computes:
--   - final_id: Explicit ID or deterministic fallback UUID v5
--   - Unnests sort_keys array for multi-phase execution
--   - Assigns sequential execution_order
--
-- Files are ordered by: sort_key ASC, path ASC (deterministic)
-- Files with multiple sort keys execute multiple times at different stages.

CREATE OR REPLACE TEMP VIEW pgmi_plan_view AS
SELECT
    -- File identity
    s.path,
    s.content,
    s.pgmi_checksum AS checksum,

    -- Metadata (with fallback for files without metadata)
    -- Fallback uses MD5 hash cast to UUID (built-in, no extension required)
    -- Note: Not RFC 4122 compliant, but consistent with deploy.sql and available during session init
    md5(s.path::bytea)::uuid AS generic_id,
    m.id,  -- NULL for files without metadata
    COALESCE(m.idempotent, true) AS idempotent,
    COALESCE(m.description, '') AS description,

    -- UNNEST sort keys: each key becomes a separate execution entry
    unnested.sort_key,

    -- Assign sequential execution order (deterministic tie-breaking with path)
    ROW_NUMBER() OVER (ORDER BY unnested.sort_key, s.path) AS execution_order

FROM pg_temp.pgmi_source s
LEFT JOIN pg_temp.pgmi_source_metadata m ON s.path = m.path

-- CROSS JOIN LATERAL: For each file, expand sort_keys array
-- If no metadata: use path as fallback sort key
CROSS JOIN LATERAL UNNEST(
    COALESCE(
        NULLIF(m.sort_keys, '{}'),  -- Use metadata sort keys if present
        ARRAY[s.path]               -- Fallback: lexicographic path order
    )
) AS unnested(sort_key)

ORDER BY unnested.sort_key, s.path;

COMMENT ON VIEW pg_temp.pgmi_plan_view IS
    'Execution plan with multi-phase support via UNNEST(sort_keys).
     Files with multiple sort keys execute multiple times at different stages.
     Order: sort_key ASC, path ASC (deterministic).
     Files without metadata use path as sort key (lexicographic order).';

-- Allow access from any role context
GRANT SELECT ON TABLE pg_temp.pgmi_plan_view TO PUBLIC;




CREATE TEMP TABLE pgmi_plan
(
	ordinal INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	command_sql text NOT NULL
);

-- Grant SELECT and INSERT to allow plan building after role switch
-- Simple permission model: no privilege escalation needed for session-scoped temp table
GRANT SELECT, INSERT ON TABLE pg_temp.pgmi_plan TO PUBLIC;





-- 1️⃣ SQL file detector
CREATE OR REPLACE FUNCTION pg_temp.pgmi_is_sql_file(filename TEXT)
RETURNS BOOLEAN
IMMUTABLE
LANGUAGE SQL
AS $$
    SELECT filename ~* E'\\.(sql|ddl|dml|dql|dcl|psql|pgsql|plpgsql)$';
$$;

-- 3️⃣ File registration helper
CREATE OR REPLACE FUNCTION pg_temp.pgmi_register_file(
    in_path TEXT,
    in_content TEXT,
    in_checksum TEXT,
    in_pgmi_checksum TEXT
)
RETURNS pg_temp.pgmi_source
LANGUAGE plpgsql
AS $$
DECLARE
    v_row pg_temp.pgmi_source;
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
    v_row.parent_folder_name :=
        CASE WHEN v_row.depth > 0
             THEN v_parts[array_length(v_parts, 1) - 1]
             ELSE NULL
        END;

    -- 🧩 3. Insert & return the row
    INSERT INTO pg_temp.pgmi_source VALUES (
        v_row.path, v_row.name, v_row.directory, v_row.extension,
        v_row.depth, v_row.content, v_row.size_bytes,
        v_row.checksum, v_row.pgmi_checksum,
        v_row.path_parts, v_row.is_sql_file, v_row.parent_folder_name
    )
    RETURNING * INTO v_row;

    RETURN v_row;
END;
$$;




-- Convenience function for accessing session variables with optional default values

-- Declares a parameter with optional type validation, default value, and documentation
-- This is the NEW recommended way to configure parameters (replaces pgmi_init_params)
--
-- Features:
--   - Self-documenting: deploy.sql becomes parameter schema
--   - Type validation: int, uuid, boolean, timestamp, etc.
--   - Required checks: fail-fast if missing
--   - Default values: applied if not provided via CLI
--   - AI-friendly: schema stored in pg_temp.pgmi_parameter for introspection
--
-- Usage:
--   SELECT pgmi_declare_param('database_admin_password', required => true);
--   SELECT pgmi_declare_param('max_connections', type => 'int', default_value => '100');
--   SELECT pgmi_declare_param('env', default_value => 'development', description => 'Deployment environment');
--
-- Supported types: text, int, integer, bigint, numeric, boolean, bool, uuid, timestamp, timestamptz, name
CREATE OR REPLACE FUNCTION pg_temp.pgmi_declare_param(
    p_key TEXT,
    p_type TEXT DEFAULT 'text',
    p_default_value TEXT DEFAULT NULL,
    p_required BOOLEAN DEFAULT false,
    p_description TEXT DEFAULT NULL
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    v_value TEXT;
    v_exists BOOLEAN;
BEGIN
    -- Validate parameter key format
    IF p_key IS NULL OR trim(p_key) = '' THEN
        RAISE EXCEPTION 'Parameter key cannot be null or empty';
    END IF;

    IF p_key !~ '^\w+$' THEN
        RAISE EXCEPTION 'Invalid parameter key: "%". Keys must be alphanumeric with underscores only', p_key;
    END IF;

    IF length(p_key) > 63 THEN
        RAISE EXCEPTION 'Parameter key "%" exceeds PostgreSQL identifier limit (63 characters)', p_key;
    END IF;

    -- Validate type is supported
    IF p_type NOT IN ('text', 'int', 'integer', 'bigint', 'numeric', 'boolean', 'bool', 'uuid', 'timestamp', 'timestamptz', 'name') THEN
        RAISE EXCEPTION 'Unsupported parameter type: "%". Supported types: text, int, bigint, numeric, boolean, uuid, timestamp, timestamptz, name', p_type;
    END IF;

    -- Check if parameter exists (from CLI via auto-initialization)
    SELECT value INTO v_value
    FROM pg_temp.pgmi_parameter
    WHERE key = p_key;

    v_exists := FOUND;

    -- Handle missing parameters
    IF NOT v_exists THEN
        IF p_required AND p_default_value IS NULL THEN
            RAISE EXCEPTION 'Required parameter "%" not provided. Use: pgmi deploy . --param %=<value>', p_key, p_key
            USING HINT = format('Available parameters: %s',
                (SELECT string_agg(key, ', ' ORDER BY key) FROM pg_temp.pgmi_parameter));
        END IF;

        v_value := p_default_value;

        -- Insert new parameter with all metadata
        INSERT INTO pg_temp.pgmi_parameter (key, value, type, required, default_value, description)
        VALUES (p_key, v_value, p_type, p_required, p_default_value, p_description);

        -- Set session variable for new parameter
        IF v_value IS NOT NULL THEN
            IF length(v_value) > 8192 THEN
                RAISE EXCEPTION 'Parameter "%" value exceeds maximum length (8192 characters)', p_key;
            END IF;
            PERFORM set_config('pgmi.' || p_key, v_value, false);
        END IF;
    ELSE
        -- CLI provided value: update metadata columns only (CLI value wins)
        UPDATE pg_temp.pgmi_parameter
        SET type = p_type,
            required = p_required,
            default_value = p_default_value,
            description = p_description
        WHERE key = p_key;
    END IF;

    -- Type validation (if value exists)
    IF v_value IS NOT NULL THEN
        CASE p_type
            WHEN 'int', 'integer' THEN
                BEGIN
                    PERFORM v_value::INTEGER;
                EXCEPTION WHEN OTHERS THEN
                    RAISE EXCEPTION 'Parameter "%" must be integer, got: "%"', p_key, v_value
                    USING HINT = format('Provide numeric value like: --param %s=100', p_key);
                END;

            WHEN 'bigint' THEN
                BEGIN
                    PERFORM v_value::BIGINT;
                EXCEPTION WHEN OTHERS THEN
                    RAISE EXCEPTION 'Parameter "%" must be bigint, got: "%"', p_key, v_value
                    USING HINT = format('Provide numeric value like: --param %s=1000000', p_key);
                END;

            WHEN 'numeric' THEN
                BEGIN
                    PERFORM v_value::NUMERIC;
                EXCEPTION WHEN OTHERS THEN
                    RAISE EXCEPTION 'Parameter "%" must be numeric, got: "%"', p_key, v_value
                    USING HINT = format('Provide numeric value like: --param %s=123.45', p_key);
                END;

            WHEN 'boolean', 'bool' THEN
                IF LOWER(v_value) NOT IN ('true', 'false', 't', 'f', 'yes', 'no', 'on', 'off', '1', '0') THEN
                    RAISE EXCEPTION 'Parameter "%" must be boolean (true/false), got: "%"', p_key, v_value
                    USING HINT = format('Provide boolean value like: --param %s=true', p_key);
                END IF;

            WHEN 'uuid' THEN
                BEGIN
                    PERFORM v_value::UUID;
                EXCEPTION WHEN OTHERS THEN
                    RAISE EXCEPTION 'Parameter "%" must be valid UUID, got: "%"', p_key, v_value
                    USING HINT = format('Provide UUID like: --param %s=550e8400-e29b-41d4-a716-446655440000', p_key);
                END;

            WHEN 'timestamp', 'timestamptz' THEN
                BEGIN
                    PERFORM v_value::TIMESTAMPTZ;
                EXCEPTION WHEN OTHERS THEN
                    RAISE EXCEPTION 'Parameter "%" must be valid timestamp, got: "%"', p_key, v_value
                    USING HINT = format('Provide timestamp like: --param %s=2024-01-15T10:30:00Z', p_key);
                END;

            WHEN 'name' THEN
                -- PostgreSQL identifier (table/schema/role names)
                IF length(v_value) > 63 THEN
                    RAISE EXCEPTION 'Parameter "%" exceeds PostgreSQL identifier limit (63 characters)', p_key;
                END IF;
                -- Note: PostgreSQL allows any character in identifiers if quoted, so minimal validation

            WHEN 'text' THEN
                -- No validation needed (any string is valid)
                NULL;

            ELSE
                RAISE EXCEPTION 'Unsupported type: "%"', p_type;
        END CASE;
    END IF;

    RAISE DEBUG 'Parameter declared: "%" (type: %, required: %, value: %)',
        p_key, p_type, p_required, COALESCE(v_value, 'NULL');
END;
$$;


-- Returns the value of a parameter from session variables, or the default if not set
-- Parameters are automatically loaded from CLI (--param key=value) or declared via pgmi_declare_param()
-- Usage: SELECT pgmi_get_param('env', 'development');
CREATE OR REPLACE FUNCTION pg_temp.pgmi_get_param(
    p_key TEXT,
    p_default TEXT DEFAULT NULL
)
RETURNS TEXT
LANGUAGE sql
STABLE
AS $$
    SELECT COALESCE(current_setting('pgmi.' || p_key, true), p_default);
$$;


CREATE OR REPLACE FUNCTION pg_temp.pgmi_plan_command(command_sql text) RETURNS pg_temp.pgmi_plan AS
$$
	INSERT INTO pg_temp.pgmi_plan(command_sql)
	VALUES ($1) RETURNING *;
$$ LANGUAGE sql;


CREATE OR REPLACE FUNCTION pg_temp.pgmi_plan_do(plpgsql_code text) RETURNS pg_temp.pgmi_plan AS
$$
	SELECT pg_temp.pgmi_plan_command(
		'DO ' || txt.boundary || ' ' || $1 || ' ' || txt.boundary || ';'
	)
	FROM (SELECT '$pgmi_' || md5($1) || '$') AS txt(boundary)
$$ LANGUAGE sql;


CREATE OR REPLACE FUNCTION pg_temp.pgmi_plan_do(body text, VARIADIC args text[])
RETURNS pg_temp.pgmi_plan AS
$$
    WITH formatted AS (
        SELECT format(body, VARIADIC args) AS body_text
    ),
    delimited AS (
        SELECT
            body_text,
            '$pgmi_' || md5(body_text) || '$' AS boundary
        FROM formatted
    )
    SELECT pg_temp.pgmi_plan_command(
        'DO ' || boundary || ' ' || body_text || ' ' || boundary || ';'
    )
    FROM delimited
$$ LANGUAGE sql;

CREATE OR REPLACE FUNCTION pg_temp.pgmi_plan_file(path text) RETURNS pg_temp.pgmi_plan AS
$$
DECLARE
    v_content TEXT;
BEGIN
    SELECT f.content INTO STRICT v_content
    FROM pg_temp.pgmi_source AS f
    WHERE f.path = $1;

    RETURN pg_temp.pgmi_plan_command(v_content);
EXCEPTION
    WHEN NO_DATA_FOUND THEN
        RAISE EXCEPTION 'File "%" not found in registered files. Use pgmi_register_file() to register it first.', $1;
    WHEN TOO_MANY_ROWS THEN
        RAISE EXCEPTION 'Multiple files found with path "%" (this should not happen - check for duplicates).', $1;
END;
$$ LANGUAGE plpgsql;

-- Convenience function for simple RAISE NOTICE statements without explicit BEGIN/END blocks
CREATE OR REPLACE FUNCTION pg_temp.pgmi_plan_notice(message text)
RETURNS pg_temp.pgmi_plan AS
$$
    SELECT pg_temp.pgmi_plan_command(
        'DO $pgmi_notice$ BEGIN RAISE NOTICE ' || quote_literal(message) || '; END $pgmi_notice$;'
    )
$$ LANGUAGE sql;

-- Overload with format placeholders (uses format() syntax with %s, %I, %L)
CREATE OR REPLACE FUNCTION pg_temp.pgmi_plan_notice(message text, VARIADIC args text[])
RETURNS pg_temp.pgmi_plan AS
$$
    SELECT pg_temp.pgmi_plan_command(
        'DO $pgmi_notice$ BEGIN RAISE NOTICE ' ||
        quote_literal(format(message, VARIADIC args)) ||
        '; END $pgmi_notice$;'
    )
$$ LANGUAGE sql;
