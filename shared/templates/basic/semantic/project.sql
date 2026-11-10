CREATE OR REPLACE VIEW project_view AS
SELECT p.project_id, p.name, p.created_at,
       c.open_task_count, c.done_task_count,
       CASE WHEN c.open_task_count > 0 THEN 'active'
            WHEN c.done_task_count > 0 THEN 'finished'
            ELSE 'empty' END AS state
FROM project p
CROSS JOIN LATERAL (
    SELECT count(*) FILTER (WHERE t.status = 'open') AS open_task_count,
           count(*) FILTER (WHERE t.status = 'done') AS done_task_count
    FROM task t
    WHERE t.project_id = p.project_id
) c;

COMMENT ON VIEW project_view IS
    'One row per project with its task counts and its state: empty, active (an open task) or finished.';

CREATE OR REPLACE FUNCTION fn_project_problems(p_name text)
RETURNS TABLE (field text, reason text)
LANGUAGE sql STABLE AS $$
    SELECT field, reason FROM (VALUES
        ('name', CASE WHEN p_name IS NULL OR btrim(p_name) = '' THEN 'required'
                      WHEN EXISTS (SELECT FROM project p WHERE lower(p.name) = lower(btrim(p_name))) THEN 'duplicate' END)
    ) AS v(field, reason)
    WHERE reason IS NOT NULL;
$$;

COMMENT ON FUNCTION fn_project_problems(text) IS
    'Why fn_create_project would refuse this name: one (field, reason) row per problem, none when it is valid.';

CREATE OR REPLACE FUNCTION fn_create_project(p_name text)
RETURNS project
LANGUAGE sql AS $$
    INSERT INTO project (name)
    SELECT btrim(p_name)
    WHERE NOT EXISTS (SELECT FROM fn_project_problems(p_name))
    RETURNING *;
$$;

COMMENT ON FUNCTION fn_create_project(text) IS
    'Creates a project and returns it; returns NULL (a row of NULLs from SELECT *) when fn_project_problems names a problem.';
