/*
<pgmi-meta id="7924a821-9d17-41b1-8be4-09c5834cae98" idempotent="false">
  <description>The task table: the work of a project, open or done.</description>
</pgmi-meta>
*/
CREATE TABLE task (
    task_id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id bigint NOT NULL REFERENCES project,
    title      text NOT NULL CONSTRAINT task_title_check CHECK (btrim(title) <> ''),
    status     text NOT NULL DEFAULT 'open' CONSTRAINT task_status_check CHECK (status IN ('open', 'done')),
    done_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT task_done_at_check CHECK ((status = 'done') = (done_at IS NOT NULL))
);
CREATE INDEX task_project_id_idx ON task (project_id);
CREATE UNIQUE INDEX task_open_title_key ON task (project_id, lower(title)) WHERE status = 'open';

COMMENT ON TABLE task IS
    'Work in a project. Read through project_view; write through fn_add_task and fn_complete_task.';
COMMENT ON INDEX task_open_title_key IS
    'Two open tasks of one project never share a title; done tasks may repeat one.';
