-- Proves the tables refuse bad rows on their own, each through the named
-- constraint: a blank or case-duplicate project name, a second open task with
-- the same title, a done task without done_at.

DO $$
DECLARE
    v_project_id bigint;
    v_constraint text;
BEGIN
    INSERT INTO project (name) VALUES ('Garden') RETURNING project_id INTO STRICT v_project_id;

    BEGIN
        INSERT INTO project (name) VALUES ('  ');
        RAISE EXCEPTION 'a blank project name was accepted';
    EXCEPTION WHEN check_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'project_name_check' THEN
            RAISE EXCEPTION 'blank name: want project_name_check, got %', v_constraint;
        END IF;
    END;

    BEGIN
        INSERT INTO project (name) VALUES ('GARDEN');
        RAISE EXCEPTION 'a project name differing only in case was accepted';
    EXCEPTION WHEN unique_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'project_name_key' THEN
            RAISE EXCEPTION 'duplicate name: want project_name_key, got %', v_constraint;
        END IF;
    END;

    INSERT INTO task (project_id, title) VALUES (v_project_id, 'Water the beds');
    BEGIN
        INSERT INTO task (project_id, title) VALUES (v_project_id, 'water the beds');
        RAISE EXCEPTION 'a second open task with the same title was accepted';
    EXCEPTION WHEN unique_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'task_open_title_key' THEN
            RAISE EXCEPTION 'duplicate open title: want task_open_title_key, got %', v_constraint;
        END IF;
    END;

    UPDATE task SET status = 'done', done_at = now() WHERE project_id = v_project_id;
    INSERT INTO task (project_id, title) VALUES (v_project_id, 'Water the beds');

    BEGIN
        INSERT INTO task (project_id, title, status) VALUES (v_project_id, 'Mow', 'done');
        RAISE EXCEPTION 'a done task without done_at was accepted';
    EXCEPTION WHEN check_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'task_done_at_check' THEN
            RAISE EXCEPTION 'done without done_at: want task_done_at_check, got %', v_constraint;
        END IF;
    END;
END $$;
