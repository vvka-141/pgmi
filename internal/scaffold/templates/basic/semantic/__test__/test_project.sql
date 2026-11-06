DO $$
DECLARE
    v_project project_view;
BEGIN
    IF (fn_create_project(' ')).project_id IS NOT NULL
       OR (SELECT reason FROM fn_project_problems(' ')) IS DISTINCT FROM 'required' THEN
        RAISE EXCEPTION 'a blank name must be refused and named required';
    END IF;
    IF (fn_create_project('website')).project_id IS NOT NULL
       OR (SELECT reason FROM fn_project_problems('website')) IS DISTINCT FROM 'duplicate' THEN
        RAISE EXCEPTION 'a name taken in another case must be refused and named duplicate';
    END IF;

    SELECT * INTO STRICT v_project FROM project_view WHERE name = 'Website';
    IF (v_project.open_task_count, v_project.done_task_count, v_project.state)
       IS DISTINCT FROM (2::bigint, 0::bigint, 'active'::text) THEN
        RAISE EXCEPTION 'Website: want 2 open, 0 done, active; got %, %, %',
            v_project.open_task_count, v_project.done_task_count, v_project.state;
    END IF;

    PERFORM fn_create_project('Empty');
    IF (SELECT state FROM project_view WHERE name = 'Empty') IS DISTINCT FROM 'empty' THEN
        RAISE EXCEPTION 'a project without tasks must be empty';
    END IF;
END $$;
