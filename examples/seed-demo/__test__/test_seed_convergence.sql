-- The point of this example is convergence: after a deploy the catalog must
-- equal the seed file, not merely contain it. The invariant tests next door
-- check shape; nothing checked that steps 5 and 6 — remove departed edges,
-- deprecate departed nodes — did anything at all. Both could be deleted and
-- every other assertion here would still pass.
--
-- Comparing against the file rather than against hardcoded keys means this
-- keeps testing the behaviour after someone edits seeds/roles.json.
DO $$
DECLARE
    v_doc  jsonb;
    v_bad  text;
BEGIN
    SELECT content::jsonb INTO v_doc
    FROM pg_temp.pgmi_source_view
    WHERE path = './seeds/roles.json';

    IF v_doc IS NULL THEN
        RAISE EXCEPTION 'seeds/roles.json is not in the session';
    END IF;

    -- Every node in the file is live, with the file's description.
    SELECT string_agg(msg, '; ') INTO v_bad FROM (
        SELECT format('role %s missing', s.key) AS msg
        FROM jsonb_to_recordset(v_doc -> 'roles') AS s(key text, description text)
        WHERE NOT EXISTS (SELECT 1 FROM role r WHERE r.key = s.key)
        UNION ALL
        SELECT format('role %s deprecated but present in the file', s.key)
        FROM jsonb_to_recordset(v_doc -> 'roles') AS s(key text)
        JOIN role r ON r.key = s.key
        WHERE r.deprecated_at IS NOT NULL
        UNION ALL
        SELECT format('role %s description is %L, file says %L', s.key, r.description, s.description)
        FROM jsonb_to_recordset(v_doc -> 'roles') AS s(key text, description text)
        JOIN role r ON r.key = s.key
        WHERE r.description IS DISTINCT FROM s.description
        UNION ALL
        SELECT format('permission %s description is %L, file says %L', s.key, p.description, s.description)
        FROM jsonb_to_recordset(v_doc -> 'permissions') AS s(key text, description text)
        JOIN permission p ON p.key = s.key
        WHERE p.description IS DISTINCT FROM s.description
    ) q;
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'seed nodes did not converge: %', v_bad;
    END IF;

    -- Every live role absent from the file must have been deprecated (step 6).
    SELECT string_agg(format('role %s is live but absent from the file', r.key), '; ')
    INTO v_bad
    FROM role r
    WHERE r.deprecated_at IS NULL
      AND NOT EXISTS (
          SELECT 1 FROM jsonb_to_recordset(v_doc -> 'roles') AS s(key text)
          WHERE s.key = r.key);
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'departed roles were not deprecated: %', v_bad;
    END IF;

    -- Edges of seed-owned roles must match the file exactly, both directions:
    -- a missing grant means step 4 under-applied, a surplus one means step 5
    -- failed to converge.
    WITH desired AS (
        SELECT r.key AS role_key, g.grant_key AS perm_key
        FROM jsonb_to_recordset(v_doc -> 'roles') AS r(key text, grants text[]),
             unnest(r.grants) AS g(grant_key)
    ),
    live AS (
        SELECT ro.key AS role_key, pe.key AS perm_key
        FROM role_permission rp
        JOIN role ro ON ro.role_id = rp.role_id
        JOIN permission pe ON pe.permission_id = rp.permission_id
        WHERE ro.key IN (SELECT role_key FROM desired)
    )
    SELECT string_agg(msg, '; ') INTO v_bad FROM (
        SELECT format('missing grant %s -> %s', role_key, perm_key) AS msg
        FROM (SELECT * FROM desired EXCEPT SELECT * FROM live) d
        UNION ALL
        SELECT format('surplus grant %s -> %s', role_key, perm_key)
        FROM (SELECT * FROM live EXCEPT SELECT * FROM desired) s
    ) q;
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'seed edges did not converge: %', v_bad;
    END IF;

    RAISE NOTICE '  + catalog matches seeds/roles.json exactly';
END $$;
