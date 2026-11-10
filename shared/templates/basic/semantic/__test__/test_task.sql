DO $$
DECLARE
    v_project_id bigint;
    v_task       task;
BEGIN
    SELECT project_id INTO STRICT v_project_id FROM project WHERE name = 'Website';

    IF (fn_add_task(-1, 'Anything')).task_id IS NOT NULL
       OR (SELECT reason FROM fn_task_problems(-1, 'Anything') WHERE field = 'project_id') IS DISTINCT FROM 'not_found' THEN
        RAISE EXCEPTION 'a task for a missing project must be refused and named not_found';
    END IF;
    IF (fn_add_task(v_project_id, 'draft the copy')).task_id IS NOT NULL
       OR (SELECT reason FROM fn_task_problems(v_project_id, 'draft the copy')) IS DISTINCT FROM 'duplicate' THEN
        RAISE EXCEPTION 'a second open task with the same title must be refused and named duplicate';
    END IF;
    IF (SELECT count(*) FILTER (WHERE task_id IS NULL) || '/' || count(*) FROM fn_add_task(v_project_id, 'draft the copy')) IS DISTINCT FROM '1/1' THEN
        RAISE EXCEPTION 'a refused write must return one row of NULLs, the contract the README and the skill describe';
    END IF;

    SELECT * INTO STRICT v_task FROM task WHERE project_id = v_project_id AND title = 'Draft the copy';
    IF (fn_complete_task(v_task.task_id)).status IS DISTINCT FROM 'done' THEN
        RAISE EXCEPTION 'completing an open task must return it done';
    END IF;
    IF (fn_complete_task(v_task.task_id)).task_id IS NOT NULL THEN
        RAISE EXCEPTION 'completing a done task must return NULL';
    END IF;
    IF fn_task_problem(v_task.task_id, 'complete') IS DISTINCT FROM 'task.done'
       OR fn_task_problem(-1, 'complete') IS DISTINCT FROM 'task.not_found' THEN
        RAISE EXCEPTION 'fn_task_problem must name task.done and task.not_found';
    END IF;

    IF (fn_add_task(v_project_id, 'Draft the copy')).task_id IS NULL THEN
        RAISE EXCEPTION 'a done task''s title must be free for a new open task';
    END IF;

    PERFORM fn_complete_task(task_id) FROM task WHERE project_id = v_project_id AND status = 'open';
    IF (SELECT state FROM project_view WHERE project_id = v_project_id) IS DISTINCT FROM 'finished' THEN
        RAISE EXCEPTION 'a project whose tasks are all done must be finished';
    END IF;
END $$;
