-- ============================================================================
-- deploy/project_rules.sql
-- The rules this project's files follow, checked before anything changes:
-- where a file may deploy, its place in the plan, and run-once structure
-- versus re-run code. Defines pg_temp.deploy_plan, the files the deploy runs.
-- ============================================================================

-- A file under a dev/ folder, at any depth, deploys only with env=dev. The plan
-- loop, the file-contract check and the run-once drift check all ask this.
CREATE FUNCTION pg_temp.deploys_file(p_path text, p_env text DEFAULT current_setting('deployment.env'))
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT p_path !~ '/dev/' OR p_env = 'dev'
$$;

-- The project files the deploy loop runs, in plan order: SQL files this
-- environment deploys, without the deploy/ files deploy.sql runs by name.
CREATE TEMPORARY VIEW deploy_plan AS
SELECT COALESCE(p.id, p.generic_id) AS object_id, p.*
FROM pg_temp.pgmi_plan_view p
JOIN pg_temp.pgmi_source_view s ON s.path = p.path
WHERE s.is_sql_file AND p.path NOT LIKE './deploy/%' AND pg_temp.deploys_file(p.path);

GRANT SELECT ON pg_temp.deploy_plan TO PUBLIC;

-- Tests run in every environment, so a test under a dev/ folder would pass on
-- dev and fail on test and prod, where its objects do not exist.
DO $$
DECLARE
    v_tests text;
BEGIN
    SELECT string_agg(path, ', ' ORDER BY path) INTO v_tests
    FROM pg_temp.pgmi_test_source_view
    WHERE NOT pg_temp.deploys_file(path, 'prod');

    IF v_tests IS NOT NULL THEN
        RAISE EXCEPTION 'Test file(s) under a dev/ folder: %', v_tests
            USING HINT = 'Move them to a __test__/ outside dev/ and branch on current_setting(''deployment.env'') where behaviour differs.';
    END IF;
END $$;

-- Every SQL file states its own place in the plan. Without sortKeys pgmi
-- orders a file by its path, and './' sorts before the framework's '003/'
-- and '004/' keys, so a forgotten header ran the file before the tables and
-- types it uses existed. Stop before anything runs and name the files.
DO $$
DECLARE
    v_untagged text;
    v_multi text;
BEGIN
    -- A run-once file is logged at its first key and skipped at the others.
    SELECT string_agg(m.path, ', ' ORDER BY m.path)
    INTO v_multi
    FROM pg_temp.pgmi_source_metadata_view m
    JOIN pg_temp.pgmi_source_view s ON s.path = m.path
    WHERE s.is_sql_file AND NOT m.idempotent AND cardinality(m.sort_keys) > 1;

    IF v_multi IS NOT NULL THEN
        RAISE EXCEPTION 'Run-once file(s) with more than one sort key: %', v_multi
            USING HINT = 'A run-once file runs once, at one place in the plan: keep one key, or split the file. Several keys are for re-run files.';
    END IF;

    SELECT string_agg(DISTINCT p.path, ', ' ORDER BY p.path)
    INTO v_untagged
    FROM pg_temp.pgmi_plan_view p
    JOIN pg_temp.pgmi_source_view s ON s.path = p.path
    WHERE s.is_sql_file AND p.sort_key = p.path AND p.path NOT LIKE './deploy/%';

    IF v_untagged IS NOT NULL THEN
        RAISE EXCEPTION 'SQL file(s) without a place in the plan: %', v_untagged
            USING HINT = 'Add a <pgmi-meta> block with <sortKeys><key>005/...</key></sortKeys> (or later) at the top of each file. See README "Extending the Framework".';
    END IF;
END $$;

-- The SQL a deploy runs when it executes a file: comments become a space,
-- string literals '', and the body of a CREATE FUNCTION or PROCEDURE is
-- dropped, because its DDL runs only when the routine is called. Other
-- dollar-quoted text (a DO block, SQL passed to EXECUTE) is read like the file.
-- It walks the text in chunks that each start at - / * ' " $ or ;.
CREATE FUNCTION pg_temp.deploy_time_sql(p_sql text)
RETURNS text LANGUAGE plpgsql IMMUTABLE STRICT AS $fn$
DECLARE
    r record;
    v_out text[] := '{}';
    v_head text := '';
    v_emit text;
    v_state text := 'code';
    v_skip bool := false;
    v_depth int;
    v_escapes bool;
    v_tag text;
    v_drop bool;
    v_body text[];
BEGIN
    FOR r IN
        SELECT t, lead(t) OVER w AS nxt, lag(t) OVER w AS prv
        FROM regexp_split_to_table(p_sql, '(?=[-/*''"$;])') WITH ORDINALITY AS x(t, o)
        WINDOW w AS (ORDER BY o)
    LOOP
        v_emit := NULL;
        IF v_state = 'code' THEN
            IF v_skip THEN
                v_emit := substr(r.t, 2);
            ELSIF r.t = '-' AND left(r.nxt, 1) = '-' THEN
                v_state := 'line';
            ELSIF r.t = '/' AND left(r.nxt, 1) = '*' THEN
                v_state := 'block';
                v_depth := 1;
            ELSIF left(r.t, 1) = '''' THEN
                v_state := 'string';
                v_escapes := right(r.prv, 1) IN ('E', 'e') AND coalesce(substr(r.prv, length(r.prv) - 1, 1), '') !~ '[[:alnum:]_$]';
                v_emit := '''''';
            ELSIF left(r.t, 1) = '"' THEN
                v_state := 'ident';
                v_emit := r.t;
            ELSIF r.t ~ '^\$([[:alpha:]_][[:alnum:]_]*)?$' AND left(r.nxt, 1) = '$'
                  AND coalesce(right(r.prv, 1), '') !~ '[[:alnum:]_$]' THEN
                v_state := 'dollar';
                v_tag := r.t;
                v_drop := v_head ~* '^\s*CREATE\s+(OR\s+REPLACE\s+)?(FUNCTION|PROCEDURE)\M';
                v_body := '{}';
                v_emit := r.t || '$';
            ELSIF left(r.t, 1) = ';' THEN
                v_out := v_out || ';'::text;
                v_head := '';
                v_emit := substr(r.t, 2);
            ELSE
                v_emit := r.t;
            END IF;
            v_skip := v_state IN ('line', 'block', 'dollar');
        ELSIF v_state = 'line' THEN
            IF strpos(r.t, E'\n') > 0 THEN
                v_state := 'code';
                v_emit := substr(r.t, strpos(r.t, E'\n'));
            END IF;
            v_skip := false;
        ELSIF v_state = 'block' THEN
            IF v_skip THEN
                v_skip := false;
            ELSIF r.t = '/' AND left(r.nxt, 1) = '*' THEN
                v_depth := v_depth + 1;
                v_skip := true;
            ELSIF r.t = '*' AND left(r.nxt, 1) = '/' THEN
                v_depth := v_depth - 1;
                v_skip := true;
                IF v_depth = 0 THEN
                    v_state := 'code';
                    v_emit := ' ';
                END IF;
            END IF;
        ELSIF v_state = 'string' THEN
            IF left(r.t, 1) = '''' AND NOT (v_escapes AND length(substring(r.prv FROM '\\*$')) % 2 = 1) THEN
                IF r.t = '''' AND left(r.nxt, 1) = '''' AND NOT v_skip THEN
                    v_skip := true;
                    CONTINUE;
                END IF;
                IF NOT v_skip THEN
                    v_state := 'code';
                    v_emit := substr(r.t, 2);
                END IF;
            END IF;
            v_skip := false;
        ELSIF v_state = 'ident' THEN
            v_emit := r.t;
            IF left(r.t, 1) = '"' THEN
                IF r.t = '"' AND left(r.nxt, 1) = '"' AND NOT v_skip THEN
                    v_skip := true;
                ELSIF NOT v_skip THEN
                    v_state := 'code';
                ELSE
                    v_skip := false;
                END IF;
            END IF;
        ELSE
            IF NOT v_skip AND r.t = v_tag AND left(r.nxt, 1) = '$' THEN
                v_state := 'code';
                v_emit := CASE WHEN v_drop THEN '' ELSE pg_temp.deploy_time_sql(array_to_string(v_body, '')) END || v_tag;
            ELSIF NOT v_drop THEN
                v_body := v_body || CASE WHEN v_skip THEN substr(r.t, 2) ELSE r.t END;
                v_skip := false;
            ELSE
                v_skip := false;
            END IF;
        END IF;
        IF v_emit <> '' THEN
            v_out := v_out || v_emit;
            IF length(v_head) < 200 THEN
                v_head := v_head || v_emit;
            END IF;
        END IF;
    END LOOP;
    RETURN array_to_string(v_out, '');
END
$fn$;

-- Structure lives in run-once files (idempotent="false"), code in re-run files
-- (idempotent="true"). A table in a re-run file is silently skipped when its
-- definition changes, and its index DDL locks writers on every deploy; a
-- function in a run-once file can never be changed by a later deploy.
-- Operators and constraint triggers count as structure: neither has an
-- OR REPLACE form, so a re-run file fails on its second deploy.
-- The check reads statement heads at the start of a line in the SQL the
-- deploy runs (pg_temp.deploy_time_sql), so it does not see DDL inside a
-- function or procedure body, nor DDL built as a string for EXECUTE unless a
-- line of that string starts with it.
DO $$
DECLARE
    v_structure text;
    v_code text;
BEGIN
    WITH f AS (
        SELECT DISTINCT p.path, p.idempotent, pg_temp.deploy_time_sql(p.content) AS body
        FROM pg_temp.deploy_plan p
    ),
    hit AS (
        SELECT DISTINCT f.path, f.idempotent, upper(regexp_replace(m[1], '\s+', ' ', 'g')) AS head
        FROM f
        CROSS JOIN LATERAL regexp_matches(f.body,
            '^\s*(CREATE\s+(UNIQUE\s+|UNLOGGED\s+)?(TABLE|INDEX|TYPE|DOMAIN|SEQUENCE|POLICY)'
            '|CREATE\s+OPERATOR|CREATE\s+CONSTRAINT\s+TRIGGER'
            '|ALTER\s+TABLE'
            '|CREATE\s+(OR\s+REPLACE\s+)?(FUNCTION|PROCEDURE|VIEW))\M', 'gin') m
    ),
    wrong AS (
        SELECT path, idempotent, string_agg(head, ', ' ORDER BY head) AS heads
        FROM hit
        WHERE idempotent <> (head ~ '(FUNCTION|PROCEDURE|VIEW)$')
        GROUP BY path, idempotent
    )
    SELECT
        string_agg(path || ' (' || heads || ')', '; ' ORDER BY path) FILTER (WHERE idempotent),
        string_agg(path || ' (' || heads || ')', '; ' ORDER BY path) FILTER (WHERE NOT idempotent)
    INTO v_structure, v_code
    FROM wrong;

    IF v_structure IS NOT NULL THEN
        RAISE EXCEPTION 'Re-run file(s) define structure: %', v_structure
            USING HINT = 'Move tables, indexes, types, domains, sequences, policies, operators, constraint triggers and ALTER TABLE into a file with idempotent="false".';
    END IF;
    IF v_code IS NOT NULL THEN
        RAISE EXCEPTION 'Run-once file(s) define code: %', v_code
            USING HINT = 'Move functions, procedures and views into a file with idempotent="true".';
    END IF;
END $$;
