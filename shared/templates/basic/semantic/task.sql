CREATE OR REPLACE FUNCTION fn_task_problems(p_project_id bigint, p_title text)
RETURNS TABLE (field text, reason text)
LANGUAGE sql STABLE AS $$
    SELECT field, reason FROM (VALUES
        ('project_id', CASE WHEN NOT EXISTS (SELECT FROM project p WHERE p.project_id = p_project_id) THEN 'not_found' END),
        ('title', CASE WHEN p_title IS NULL OR btrim(p_title) = '' THEN 'required'
                       WHEN EXISTS (SELECT FROM task t
                                    WHERE t.project_id = p_project_id AND t.status = 'open'
                                      AND lower(t.title) = lower(btrim(p_title))) THEN 'duplicate' END)
    ) AS v(field, reason)
    WHERE reason IS NOT NULL;
$$;

COMMENT ON FUNCTION fn_task_problems(bigint, text) IS
    'Why fn_add_task would refuse: one (field, reason) row per problem, none when it is valid.';

CREATE OR REPLACE FUNCTION fn_task_problem(p_task_id bigint, p_operation text)
RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN t.task_id IS NULL                                THEN 'task.not_found'
        WHEN p_operation = 'complete' AND t.status = 'done'   THEN 'task.done'
    END
    FROM (SELECT p_task_id) k
    LEFT JOIN task t ON t.task_id = p_task_id;
$$;

COMMENT ON FUNCTION fn_task_problem(bigint, text) IS
    'Why an operation on a task would be refused (task.not_found, task.done), or NULL when it is allowed.';

CREATE OR REPLACE FUNCTION fn_add_task(p_project_id bigint, p_title text)
RETURNS task
LANGUAGE sql AS $$
    INSERT INTO task (project_id, title)
    SELECT p_project_id, btrim(p_title)
    WHERE NOT EXISTS (SELECT FROM fn_task_problems(p_project_id, p_title))
    RETURNING *;
$$;

COMMENT ON FUNCTION fn_add_task(bigint, text) IS
    'Adds an open task and returns it; returns NULL (a row of NULLs from SELECT *) when fn_task_problems names a problem.';

CREATE OR REPLACE FUNCTION fn_complete_task(p_task_id bigint)
RETURNS task
LANGUAGE sql AS $$
    UPDATE task SET status = 'done', done_at = now()
    WHERE task_id = p_task_id
      AND status = 'open'
      AND (SELECT fn_task_problem(p_task_id, 'complete')) IS NULL
    RETURNING *;
$$;

COMMENT ON FUNCTION fn_complete_task(bigint) IS
    'Marks an open task done and returns it; returns NULL when fn_task_problem refuses. status = ''open'' is the race guard: after a lock wait PostgreSQL re-checks only the row''s own columns.';
