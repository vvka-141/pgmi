BEGIN;

-- 1. Validate the seed file before touching any table
DO $$
DECLARE
    v_missing text;
    v_dup     text;
BEGIN
    WITH doc AS (
        SELECT content::jsonb AS j
        FROM pg_temp.pgmi_source_view
        WHERE path = './seeds/roles.json'
    ),
    granted AS (
        SELECT DISTINCT g.grant_key
        FROM doc,
             jsonb_to_recordset(doc.j -> 'roles') AS r(key text, grants text[]),
             unnest(r.grants) AS g(grant_key)
    ),
    declared AS (
        SELECT p.key
        FROM doc, jsonb_to_recordset(doc.j -> 'permissions') AS p(key text)
    )
    SELECT string_agg(grant_key, ', ') INTO v_missing
    FROM granted g
    WHERE NOT EXISTS (SELECT 1 FROM declared d WHERE d.key = g.grant_key);

    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'seed file grants undeclared permissions: %', v_missing;
    END IF;

    WITH doc AS (
        SELECT content::jsonb AS j
        FROM pg_temp.pgmi_source_view
        WHERE path = './seeds/roles.json'
    ),
    entity AS (
        SELECT 'role' AS kind, r.key
        FROM doc, jsonb_to_recordset(doc.j -> 'roles') AS r(key text)
        UNION ALL
        SELECT 'permission', p.key
        FROM doc, jsonb_to_recordset(doc.j -> 'permissions') AS p(key text)
    )
    SELECT string_agg(kind || ' ' || key, ', ') INTO v_dup
    FROM (
        SELECT kind, key FROM entity
        GROUP BY kind, key HAVING count(*) > 1
    ) d;

    IF v_dup IS NOT NULL THEN
        RAISE EXCEPTION 'duplicate keys in seed file: %', v_dup;
    END IF;

    RAISE NOTICE 'seed file validated: ./seeds/roles.json';
END $$;

-- 2. Apply migrations in path order
DO $$
DECLARE
    v_file record;
BEGIN
    FOR v_file IN
        SELECT path, content
        FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    LOOP
        RAISE NOTICE 'applying %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

-- 3. Pre-apply diff: what THIS deploy will change, reported BEFORE any mutation
--    (attribute drift, added edges, removed edges, deprecated nodes)
DO $$
DECLARE
    v_row record;
BEGIN
    FOR v_row IN
        WITH doc AS (
            SELECT content::jsonb AS j
            FROM pg_temp.pgmi_source_view
            WHERE path = './seeds/roles.json'
        ),
        seed_role AS (
            SELECT r.key, r.description
            FROM doc, jsonb_to_recordset(doc.j -> 'roles') AS r(key text, description text)
        ),
        seed_permission AS (
            SELECT p.key, p.description
            FROM doc, jsonb_to_recordset(doc.j -> 'permissions') AS p(key text, description text)
        ),
        seed_edge AS (
            SELECT r.key AS role_key, g.grant_key AS perm_key
            FROM doc, jsonb_to_recordset(doc.j -> 'roles') AS r(key text, grants text[]),
                 unnest(r.grants) AS g(grant_key)
        )
        SELECT format('role %s: live=%L seed=%L', r.key, r.description, s.description) AS msg
        FROM role r JOIN seed_role s USING (key)
        WHERE r.description IS DISTINCT FROM s.description
        UNION ALL
        SELECT format('permission %s: live=%L seed=%L', p.key, p.description, s.description)
        FROM permission p JOIN seed_permission s USING (key)
        WHERE p.description IS DISTINCT FROM s.description
        UNION ALL
        SELECT format('grant + %s -> %s', se.role_key, se.perm_key)
        FROM seed_edge se
        WHERE NOT EXISTS (
            SELECT 1 FROM role_permission rp
            JOIN role ro ON ro.role_id = rp.role_id
            JOIN permission pe ON pe.permission_id = rp.permission_id
            WHERE ro.key = se.role_key AND pe.key = se.perm_key)
        UNION ALL
        SELECT format('grant - %s -> %s', ro.key, pe.key)
        FROM role_permission rp
        JOIN role ro ON ro.role_id = rp.role_id
        JOIN permission pe ON pe.permission_id = rp.permission_id
        WHERE ro.key IN (SELECT key FROM seed_role)
          AND NOT EXISTS (SELECT 1 FROM seed_edge se
                          WHERE se.role_key = ro.key AND se.perm_key = pe.key)
        UNION ALL
        SELECT format('role - %s (deprecate)', r.key)
        FROM role r
        WHERE r.deprecated_at IS NULL
          AND NOT EXISTS (SELECT 1 FROM seed_role s WHERE s.key = r.key)
    LOOP
        RAISE NOTICE 'plan: %', v_row.msg;
    END LOOP;
END $$;

-- 4. Upsert nodes and add missing edges: one statement, RETURNING-chained
WITH doc AS (
    SELECT content::jsonb AS j
    FROM pg_temp.pgmi_source_view
    WHERE path = './seeds/roles.json'
),
new_permission AS (
    INSERT INTO permission (key, description)
    SELECT p.key, p.description
    FROM doc,
         jsonb_to_recordset(doc.j -> 'permissions')
             AS p(key text, description text)
    ON CONFLICT (key) DO UPDATE
        SET description = EXCLUDED.description,
            deprecated_at = NULL
    RETURNING permission_id, key
),
new_role AS (
    INSERT INTO role (key, description)
    SELECT r.key, r.description
    FROM doc,
         jsonb_to_recordset(doc.j -> 'roles')
             AS r(key text, description text)
    ON CONFLICT (key) DO UPDATE
        SET description = EXCLUDED.description,
            deprecated_at = NULL
    RETURNING role_id, key
)
INSERT INTO role_permission (role_id, permission_id)
SELECT nr.role_id, np.permission_id
FROM doc,
     jsonb_to_recordset(doc.j -> 'roles') AS r(key text, grants text[]),
     new_role nr,
     new_permission np
WHERE nr.key = r.key
  AND np.key = ANY (r.grants)
ON CONFLICT DO NOTHING;

-- 5. Converge edges: delete grants of seed-owned roles that left the file
WITH doc AS (
    SELECT content::jsonb AS j
    FROM pg_temp.pgmi_source_view
    WHERE path = './seeds/roles.json'
),
seed_role AS (
    SELECT r.key FROM doc, jsonb_to_recordset(doc.j -> 'roles') AS r(key text)
),
desired AS (
    SELECT ro.role_id, pe.permission_id
    FROM doc,
         jsonb_to_recordset(doc.j -> 'roles') AS r(key text, grants text[]),
         unnest(r.grants) AS g(grant_key),
         role ro, permission pe
    WHERE ro.key = r.key AND pe.key = g.grant_key
)
DELETE FROM role_permission rp
USING role ro
WHERE rp.role_id = ro.role_id
  AND ro.key IN (SELECT key FROM seed_role)
  AND NOT EXISTS (SELECT 1 FROM desired d
                  WHERE d.role_id = rp.role_id AND d.permission_id = rp.permission_id);

-- 6. Deprecate nodes that left the file (soft: identity and history preserved)
WITH doc AS (
    SELECT content::jsonb AS j
    FROM pg_temp.pgmi_source_view
    WHERE path = './seeds/roles.json'
),
seed_role AS (
    SELECT r.key FROM doc, jsonb_to_recordset(doc.j -> 'roles') AS r(key text)
)
UPDATE role SET deprecated_at = now()
WHERE deprecated_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM seed_role s WHERE s.key = role.key);

WITH doc AS (
    SELECT content::jsonb AS j
    FROM pg_temp.pgmi_source_view
    WHERE path = './seeds/roles.json'
),
seed_permission AS (
    SELECT p.key FROM doc, jsonb_to_recordset(doc.j -> 'permissions') AS p(key text)
)
UPDATE permission SET deprecated_at = now()
WHERE deprecated_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM seed_permission s WHERE s.key = permission.key);

-- 7. Gate the deploy on catalog invariants
SAVEPOINT _tests;
CALL pgmi_test();
ROLLBACK TO SAVEPOINT _tests;

COMMIT;
