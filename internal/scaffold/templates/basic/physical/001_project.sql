/*
<pgmi-meta id="a7ba0c95-d9fc-44f9-8537-de2631a38a8a" idempotent="false">
  <description>Projects</description>
</pgmi-meta>
*/
CREATE TABLE project (
    project_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text NOT NULL CONSTRAINT project_name_check CHECK (btrim(name) <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX project_name_key ON project (lower(name));

COMMENT ON TABLE project IS
    'A body of work that holds tasks. Read project_view; write through fn_create_project.';
