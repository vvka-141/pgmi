# Worked handler bodies

Create, PATCH and delete handlers over a `task` entity, and a test that
calls each. To keep them runnable, this page deploys the table, kernels and
handlers as a test fixture that is rolled back. In a project they belong in
the four files of the pgmi-endpoint skill.

## Contents

1. The fixture: table, kernels, handlers
2. What each handler shows
3. The test

## The fixture

```sql runnable target=api/__test__/handler_examples/_setup.sql
CREATE TABLE core.task (
    object_id       core.entity_id PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    organization_id uuid NOT NULL DEFAULT api.current_organization_id(),
    parent_task_id  uuid,
    title           text NOT NULL,
    estimate        numeric(6,1) CONSTRAINT task_estimate_check CHECK (estimate > 0),
    note            text,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT task_organization_object_key UNIQUE (organization_id, object_id),
    CONSTRAINT task_parent_fkey FOREIGN KEY (organization_id, parent_task_id)
        REFERENCES core.task (organization_id, object_id)
);
CREATE INDEX ix_task_parent ON core.task (organization_id, parent_task_id);
SELECT pg_temp.apply_entity_table_standards('core.task');
CREATE UNIQUE INDEX uq_task_organization_title ON core.task (organization_id, lower(title)) WHERE deleted_at IS NULL;
SELECT core.apply_org_rls('core.task', p_has_created_by => false);

SELECT core.ensure_view('core.vw_task', $view$
    CREATE OR REPLACE VIEW core.vw_task WITH (security_invoker = true) AS
    SELECT t.*,
           (SELECT count(*) FROM core.task s WHERE s.parent_task_id = t.object_id AND s.deleted_at IS NULL) AS subtask_count
    FROM core.task t
    WHERE t.deleted_at IS NULL
$view$);

CREATE FUNCTION core.fn_task_problem(p_object_id uuid, p_operation text)
RETURNS text LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN t.object_id IS NULL                                THEN 'core.task.not_found'
        WHEN p_operation = 'delete' AND t.subtask_count > 0     THEN 'core.task.has_subtasks'
    END
    FROM (SELECT p_object_id) k
    LEFT JOIN core.vw_task t ON t.object_id = p_object_id;
$$;

CREATE FUNCTION core.fn_task_problems(p_title text, p_estimate numeric, p_parent uuid, p_object_id uuid DEFAULT NULL)
RETURNS TABLE (field text, reason text) LANGUAGE sql STABLE AS $$
    SELECT field, reason FROM (VALUES
        ('title', CASE WHEN p_title IS NULL OR btrim(p_title) = '' THEN 'required'
                       WHEN EXISTS (SELECT FROM core.vw_task v WHERE lower(v.title) = lower(btrim(p_title))
                                                               AND v.object_id IS DISTINCT FROM p_object_id)
                           THEN 'duplicate' END),
        ('estimate', CASE WHEN p_estimate <= 0 THEN 'invalid' END),
        ('parentTaskId', CASE WHEN p_parent IS NOT NULL
                               AND NOT EXISTS (SELECT FROM core.vw_task v WHERE v.object_id = p_parent)
                              THEN 'not_found' END)
    ) AS p(field, reason)
    WHERE reason IS NOT NULL;
$$;

CREATE FUNCTION core.fn_create_task(p_title text, p_estimate numeric, p_parent uuid)
RETURNS core.task LANGUAGE sql AS $$
    INSERT INTO core.task (title, estimate, parent_task_id)
    SELECT btrim(p_title), p_estimate, p_parent
    WHERE NOT EXISTS (SELECT FROM core.fn_task_problems(p_title, p_estimate, p_parent))
    ON CONFLICT DO NOTHING
    RETURNING *;
$$;

CREATE FUNCTION core.fn_task_patched(t core.task, p_patch jsonb)
RETURNS core.task LANGUAGE sql STABLE AS $$
    SELECT jsonb_populate_record(t, COALESCE(
        (SELECT jsonb_object_agg(k, v) FROM jsonb_each(p_patch) AS e(k, v)
          WHERE k IN ('title', 'estimate', 'note')), '{}'));
$$;

CREATE FUNCTION core.fn_task_patch_problems(p_id uuid, p_patch jsonb)
RETURNS TABLE (field text, reason text) LANGUAGE sql STABLE AS $$
    SELECT p.field, p.reason
    FROM core.task t
    CROSS JOIN LATERAL core.fn_task_patched(t, p_patch) n
    CROSS JOIN LATERAL core.fn_task_problems(n.title, n.estimate, n.parent_task_id, n.object_id) p
    WHERE t.object_id = p_id AND t.deleted_at IS NULL;
$$;

CREATE FUNCTION core.fn_patch_task(p_id uuid, p_patch jsonb)
RETURNS core.task LANGUAGE sql AS $$
    UPDATE core.task t SET (title, estimate, note, updated_at) =
        (SELECT btrim(n.title), n.estimate, n.note, now() FROM core.fn_task_patched(t, p_patch) n)
    WHERE t.object_id = p_id AND t.deleted_at IS NULL
      AND NOT EXISTS (SELECT FROM core.fn_task_patch_problems(p_id, p_patch))
    RETURNING *;
$$;

CREATE FUNCTION core.fn_archive_task(p_id uuid)
RETURNS core.task LANGUAGE sql AS $$
    UPDATE core.task SET deleted_at = now()
    WHERE object_id = p_id AND deleted_at IS NULL
      AND core.fn_task_problem(p_id, 'delete') IS NULL
    RETURNING *;
$$;

CREATE FUNCTION core.fn_task_json(t core.task)
RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_build_object('id', t.object_id, 'title', t.title, 'estimate', t.estimate,
                              'note', t.note, 'parentTaskId', t.parent_task_id);
$$;

SELECT api.create_or_replace_schema_component('Task', '{"type": "object"}'::jsonb);

-- Create: malformed values, then every rule at once, then the kernel
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'fe204978-7c14-495b-914b-da22d6473693',
        'name', 'create_task', 'path', '/tasks', 'httpMethod', '^POST$',
        'access', 'organization', 'successStatus', 201,
        'noLocation', 'this example has no GET /tasks/{id}; add one and declare locationTemplate instead',
        'inputSchema', '{"type": "object", "required": ["title"], "properties": {"title": {"type": "string"}, "estimate": {"type": "number"}, "parentTaskId": {"type": "string"}}}'::jsonb,
        'outputSchema', '{"$ref": "#/components/schemas/Task"}'::jsonb),
    $body$
DECLARE
    v_b        jsonb := api.content_json((request).content);
    v_bad      jsonb := api.malformed_params(v_b, '{"parentTaskId": "uuid"}');
    v_title    text := v_b->>'title';
    v_estimate numeric := v_b->>'estimate' ?> NULL::numeric;
    v_parent   uuid := v_b->>'parentTaskId' ?> NULL::uuid;
    v_problems jsonb;
    v_row      core.task;
BEGIN
    IF v_bad IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Entity', 'Some values are malformed', invalid_params => v_bad);
    END IF;

    SELECT jsonb_agg(api.invalid_param(field, reason) ORDER BY field) INTO v_problems
    FROM core.fn_task_problems(v_title, v_estimate, v_parent);
    IF v_problems IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Entity', 'Some values are not accepted', invalid_params => v_problems);
    END IF;

    v_row := core.fn_create_task(v_title, v_estimate, v_parent);
    IF v_row.object_id IS NULL THEN
        SELECT jsonb_agg(api.invalid_param(field, reason) ORDER BY field) INTO v_problems
        FROM core.fn_task_problems(v_title, v_estimate, v_parent);
        IF v_problems IS NOT NULL THEN
            RETURN api.problem_response(422, 'Unprocessable Entity', 'Some values are not accepted', invalid_params => v_problems);
        END IF;
        RETURN api.problem_response(409, 'Conflict', 'The tasks changed meanwhile; try again', code => 'core.task.changed');
    END IF;
    RETURN api.json_response(201, core.fn_task_json(v_row));
END;
    $body$);

-- PATCH: change only what the body names
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', '7afd20ab-f09c-4a68-9928-709fe69219e4',
        'name', 'patch_task', 'path', '/tasks/{id:uuid}', 'httpMethod', '^PATCH$',
        'access', 'organization',
        'inputSchema', '{"type": "object", "properties": {"title": {"type": "string"}, "estimate": {"type": ["number", "null"]}, "note": {"type": ["string", "null"]}}}'::jsonb,
        'outputSchema', '{"$ref": "#/components/schemas/Task"}'::jsonb),
    $body$
DECLARE
    v_id       uuid := ((request).path_params->>'id')::uuid;
    v_b        jsonb := api.content_json((request).content);
    v_problems jsonb;
    v_row      core.task;
BEGIN
    IF core.fn_task_problem(v_id, 'patch') = 'core.task.not_found' THEN
        RETURN api.problem_response(404, 'Not Found', 'Task not found', code => 'core.task.not_found');
    END IF;

    SELECT jsonb_agg(api.invalid_param(field, reason) ORDER BY field) INTO v_problems
    FROM core.fn_task_patch_problems(v_id, v_b);
    IF v_problems IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Entity', 'Some values are not accepted', invalid_params => v_problems);
    END IF;

    v_row := core.fn_patch_task(v_id, v_b);
    IF v_row.object_id IS NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'The task changed meanwhile; try again', code => 'core.task.changed');
    END IF;
    RETURN api.json_response(200, core.fn_task_json(v_row));
END;
    $body$);

-- Delete: one problem function decides 404 or 409, the kernel re-checks it
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', '5a3f8e4c-2b7d-4e1a-9c6f-0d8b7a2e4f13',
        'name', 'delete_task', 'path', '/tasks/{id:uuid}', 'httpMethod', '^DELETE$',
        'access', 'organization', 'successStatus', 204,
        'outputSchema', '{"type": "object"}'::jsonb),
    $body$
DECLARE
    v_id      uuid := ((request).path_params->>'id')::uuid;
    v_problem text := core.fn_task_problem(v_id, 'delete');
BEGIN
    IF v_problem IS NOT NULL THEN
        RETURN CASE v_problem
            WHEN 'core.task.not_found' THEN api.problem_response(404, 'Not Found', 'Task not found', code => 'core.task.not_found')
            WHEN 'core.task.has_subtasks' THEN api.problem_response(409, 'Conflict', 'The task still has subtasks', code => 'core.task.has_subtasks')
        END;
    END IF;
    IF (core.fn_archive_task(v_id)).object_id IS NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'The task changed meanwhile; try again', code => 'core.task.changed');
    END IF;
    RETURN (204, ''::extensions.hstore, NULL)::api.http_response;
END;
    $body$);
```

## What each handler shows

- **Create.** The dispatcher refused a missing `title` and a non-number
  `estimate` (400). The body refuses a malformed `parentTaskId` (422), then
  asks `core.fn_task_problems` for every rule at once: a blank or duplicate
  title, a non-positive estimate, a parent that names nothing. The kernel
  writes only `WHERE NOT EXISTS` the same problems, and `ON CONFLICT DO
  NOTHING`: two requests can both pass the probe before either commits, and
  `uq_task_organization_title` makes the second insert wait, then do nothing.
  On a NULL result the body asks the problem function again; the winner's
  row is visible now, so a raced duplicate gets the same 422 as a late one.
  Only a state that changed some other way is 409.
- **PATCH.** `core.fn_task_patched` lays the body over the stored row:
  a field the body names changes, `null` clears, an absent one stays.
  `core.fn_task_patch_problems` asks `core.fn_task_problems` about the task
  as it would be after the patch, so a blank title or a non-positive
  estimate is refused by the same rule as on create, and the task's own
  title is not a duplicate of itself. The kernel writes only when the same
  function names nothing.
- **Delete.** `core.fn_task_problem` answers `core.task.not_found` (404, also
  for a task row-level security hides) or `core.task.has_subtasks` (409) in one call;
  the `CASE` has one arm per code it can return, each with a literal code.
  Without that probe the foreign key would still refuse a hard delete, but
  the dispatcher's floor would answer a generic `pgmi.resource_in_use`.

## The test

```sql runnable target=api/__test__/handler_examples/test_handler_examples.sql
DO $$
DECLARE
    v_h    extensions.hstore := pg_temp.test_session_headers(current_setting('test.alice_subject'),
                                                              current_setting('test.acme_org_id')::uuid);
    v_resp api.http_response;
    v_task jsonb;
    v_child jsonb;
BEGIN
    v_resp := api.rest_invoke('POST', '/tasks', v_h, '{"title": "Draft", "estimate": 2}'::jsonb);
    v_task := api.content_json((v_resp).content);
    IF (v_resp).status_code IS DISTINCT FROM 201 THEN
        RAISE EXCEPTION 'create: want 201, got % %', (v_resp).status_code, v_task;
    END IF;

    v_resp := api.rest_invoke('POST', '/tasks', v_h, '{"title": "Child", "parentTaskId": "nope"}'::jsonb);
    IF (v_resp).status_code IS DISTINCT FROM 422 THEN
        RAISE EXCEPTION 'malformed parent: want 422, got %', (v_resp).status_code;
    END IF;
    v_resp := api.rest_invoke('POST', '/tasks', v_h,
        jsonb_build_object('title', 'draft', 'estimate', 0, 'parentTaskId', gen_random_uuid()));
    IF api.content_json((v_resp).content)->'invalid-params' IS DISTINCT FROM
       '[{"name": "estimate", "reason": "invalid"}, {"name": "parentTaskId", "reason": "not_found"}, {"name": "title", "reason": "duplicate"}]'::jsonb THEN
        RAISE EXCEPTION 'want every problem at once, got %', api.content_json((v_resp).content);
    END IF;

    v_resp := api.rest_invoke('POST', '/tasks', v_h, jsonb_build_object('title', 'Child', 'parentTaskId', v_task->>'id'));
    v_child := api.content_json((v_resp).content);
    v_resp := api.rest_invoke('DELETE', '/tasks/' || (v_task->>'id'), v_h, NULL::bytea);
    IF (v_resp).status_code IS DISTINCT FROM 409
       OR api.content_json((v_resp).content)->>'code' IS DISTINCT FROM 'core.task.has_subtasks' THEN
        RAISE EXCEPTION 'delete with a subtask: want 409 core.task.has_subtasks, got %', (v_resp).status_code;
    END IF;
    v_resp := api.rest_invoke('DELETE', '/tasks/' || (v_child->>'id'), v_h, NULL::bytea);

    v_resp := api.rest_invoke('PATCH', '/tasks/' || (v_task->>'id'), v_h, '{"title": " ", "estimate": 0}'::jsonb);
    IF (v_resp).status_code IS DISTINCT FROM 422
       OR api.content_json((v_resp).content)->'invalid-params' IS DISTINCT FROM
          '[{"name": "estimate", "reason": "invalid"}, {"name": "title", "reason": "required"}]'::jsonb THEN
        RAISE EXCEPTION 'PATCH blank title and zero estimate: want 422 naming both, got % %',
            (v_resp).status_code, api.content_json((v_resp).content);
    END IF;
    v_resp := api.rest_invoke('PATCH', '/tasks/' || (v_task->>'id'), v_h, '{"title": "Draft"}'::jsonb);
    IF (v_resp).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'PATCH to its own title: want 200, got % %', (v_resp).status_code, api.content_json((v_resp).content);
    END IF;

    v_resp := api.rest_invoke('PATCH', '/tasks/' || (v_task->>'id'), v_h, '{"note": "first"}'::jsonb);
    v_resp := api.rest_invoke('PATCH', '/tasks/' || (v_task->>'id'), v_h, '{"note": null}'::jsonb);
    v_task := api.content_json((v_resp).content);
    IF (v_resp).status_code IS DISTINCT FROM 200 OR v_task->'note' IS DISTINCT FROM 'null'::jsonb
       OR (v_task->>'estimate')::numeric IS DISTINCT FROM 2 THEN
        RAISE EXCEPTION 'PATCH must clear note and keep estimate, got % %', (v_resp).status_code, v_task;
    END IF;

    v_resp := api.rest_invoke('DELETE', '/tasks/' || (v_task->>'id'), v_h, NULL::bytea);
    IF (v_resp).status_code IS DISTINCT FROM 204 THEN
        RAISE EXCEPTION 'delete: want 204, got %', (v_resp).status_code;
    END IF;
    v_resp := api.rest_invoke('DELETE', '/tasks/' || (v_task->>'id'), v_h, NULL::bytea);
    IF (v_resp).status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION 'second delete: want 404, got %', (v_resp).status_code;
    END IF;
END $$;
```
