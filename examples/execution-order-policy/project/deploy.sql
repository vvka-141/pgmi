-- deploy.sql — phased catalog load.
-- The plan is a query result: print it, assert it, then execute it.

BEGIN;

-- 1. The plan, as data
DO $$
DECLARE
    v_row RECORD;
BEGIN
    RAISE NOTICE 'deployment plan:';
    FOR v_row IN
        SELECT execution_order, sort_key, path
        FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    LOOP
        RAISE NOTICE '  #% | % | %', v_row.execution_order, v_row.sort_key, v_row.path;
    END LOOP;
END $$;

-- 2. Ordering policy: no two files may claim the same plan position
DO $$
DECLARE
    v_collisions TEXT;
BEGIN
    SELECT string_agg(format('%s: %s', sort_key, paths), E'\n' ORDER BY sort_key)
      INTO v_collisions
      FROM (
          SELECT sort_key, string_agg(path, ', ' ORDER BY path) AS paths
          FROM pg_temp.pgmi_plan_view
          GROUP BY sort_key
          HAVING count(*) > 1
      ) AS collision;

    IF v_collisions IS NOT NULL THEN
        RAISE EXCEPTION E'duplicate plan positions:\n%', v_collisions
            USING HINT = 'Assign each file a distinct sort key, or drop this check if ties are intentional.';
    END IF;
END $$;

-- 3. Assert the plan against the reviewed manifest
DO $$
DECLARE
    v_diff TEXT;
BEGIN
    WITH manifest(execution_order, sort_key, path) AS (
        VALUES
            (1::BIGINT, '100/010', './schema/010_catalog.sql'),
            (2,         '150/000', './checks/smoke.sql'),
            (3,         '200/010', './load/010_products.sql'),
            (4,         '200/020', './load/020_prices.sql'),
            (5,         '300/010', './post/010_indexes.sql'),
            (6,         '400/000', './checks/smoke.sql')
    )
    SELECT string_agg(
               CASE
                   WHEN m.execution_order IS NULL THEN
                       format('#%s: not in manifest — plan has (%s, %s)',
                              p.execution_order, p.sort_key, p.path)
                   WHEN p.execution_order IS NULL THEN
                       format('#%s: missing from plan — manifest has (%s, %s)',
                              m.execution_order, m.sort_key, m.path)
                   ELSE
                       format('#%s: manifest has (%s, %s), plan has (%s, %s)',
                              m.execution_order, m.sort_key, m.path, p.sort_key, p.path)
               END,
               E'\n' ORDER BY COALESCE(m.execution_order, p.execution_order))
      INTO v_diff
      FROM manifest m
      FULL JOIN pg_temp.pgmi_plan_view p USING (execution_order)
     WHERE ROW(m.sort_key, m.path) IS DISTINCT FROM ROW(p.sort_key, p.path);

    IF v_diff IS NOT NULL THEN
        RAISE EXCEPTION E'plan does not match the reviewed manifest:\n%', v_diff
            USING HINT = 'Review the change, then update the manifest in deploy.sql.';
    END IF;
END $$;

-- 4. Execute
DO $$
DECLARE
    v_step RECORD;
BEGIN
    FOR v_step IN
        SELECT execution_order, sort_key, path, content
        FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    LOOP
        RAISE NOTICE 'executing #% % (sort_key %)',
            v_step.execution_order, v_step.path, v_step.sort_key;
        EXECUTE v_step.content;
    END LOOP;
END $$;

COMMIT;
