# A worked endpoint: projects

Four files add a `project` entity with `POST /projects`, `GET /projects` and
`GET /projects/{id}`. Each block deploys as written. Give every file you copy
a new `id` (`uuidgen`); two files with one id stop the deploy with exit 10.

## Contents

1. Structure: `core/project-structure.sql`
2. Kernel: `core/project.sql`
3. Handlers: `api/project-handlers.sql`
4. Test: `api/__test__/test_project.sql`

## 1. Structure

Run once (`idempotent="false"`), plain DDL. Never edit this file after it ran
on a database: change the table with a new run-once file sorted after it.

```sql runnable target=core/project-structure.sql
/*
<pgmi-meta id="f7f4a738-3c2a-496e-bad5-0e253e42fc40" idempotent="false">
  <description>The project table, one organization's rows.</description>
  <sortKeys><key>005/100/project/1</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.project (
    object_id          core.entity_id PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    organization_id    uuid NOT NULL,
    created_by_user_id uuid NOT NULL,
    name               text NOT NULL CONSTRAINT project_name_not_blank CHECK (btrim(name) <> ''),
    due_on             date
);

CREATE INDEX ix_project_organization_name ON core.project (organization_id, name);

SELECT pg_temp.apply_entity_table_standards('core.project');
SELECT core.apply_org_rls('core.project');
```

- `object_id core.entity_id` marks an entity: `created_at` and `deleted_at`
  are added by `pg_temp.apply_entity_table_standards`. Calling it here, not
  leaving it to the deploy-end sweep, lets the kernel below name the columns
  on the first deploy.
- `core.apply_org_rls` forces row-level security with the four policies keyed
  on `api.current_organization_id()`, and checks `created_by_user_id` on
  insert. A table that holds no organization's rows calls
  `pg_temp.exempt_from_rls('core.<table>', '<reason>')` instead.

## 2. Kernel

Re-run (`idempotent="true"`): functions and views, replaced on every deploy.
`core.fn_project_problems` states the project's rules once: the kernel
writes only when it names none, and the handler asks it for the 422. The
kernel does the write and returns the whole row; the formatter turns a row
into the API's JSON. A new rule (a unique name, a due date in the future) is
one more row in the `VALUES` list, and both callers follow.

```sql runnable target=core/project.sql
/*
<pgmi-meta id="63c4bb3f-e3a5-40b9-87bb-abd6e8762b23" idempotent="true">
  <description>Project kernels: the rules, create, and the JSON shape of a project.</description>
  <sortKeys><key>005/100/project/2</key></sortKeys>
</pgmi-meta>
*/
CREATE OR REPLACE FUNCTION core.fn_project_problems(p_name text)
RETURNS TABLE (field text, reason text)
LANGUAGE sql STABLE AS $$
    SELECT field, reason FROM (VALUES
        ('name', CASE WHEN p_name IS NULL OR btrim(p_name) = '' THEN 'required' END)
    ) AS p(field, reason)
    WHERE reason IS NOT NULL;
$$;

CREATE OR REPLACE FUNCTION core.fn_create_project(p_name text, p_due_on date)
RETURNS core.project
LANGUAGE sql AS $$
    INSERT INTO core.project (organization_id, created_by_user_id, name, due_on)
    SELECT api.current_organization_id(), api.current_principal_id(), btrim(p_name), p_due_on
    WHERE NOT EXISTS (SELECT FROM core.fn_project_problems(p_name))
    RETURNING *;
$$;

CREATE OR REPLACE FUNCTION core.fn_project_json(p core.project)
RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_build_object(
        'id',        p.object_id,
        'name',      p.name,
        'dueOn',     p.due_on,
        'createdAt', p.created_at);
$$;
```

Pass the row (`core.fn_project_json(p)`), not `p.*`.

## 3. Handlers

```sql runnable target=api/project-handlers.sql
/*
<pgmi-meta id="59e41294-1d6f-45bb-98eb-e32b9c53018d" idempotent="true">
  <description>Project endpoints: create, list, read one.</description>
  <sortKeys><key>005/100/project/3</key></sortKeys>
</pgmi-meta>
*/
SELECT api.create_or_replace_schema_component('Project', jsonb_build_object(
    'type', 'object',
    'properties', jsonb_build_object(
        'id',        jsonb_build_object('type', 'string', 'format', 'uuid'),
        'name',      jsonb_build_object('type', 'string'),
        'dueOn',     jsonb_build_object('type', ARRAY['string', 'null'], 'format', 'date'),
        'createdAt', jsonb_build_object('type', 'string', 'format', 'date-time')),
    'required', jsonb_build_array('id', 'name', 'dueOn', 'createdAt')));

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'd3d1ccc2-4122-4e35-8b32-04c60fecb124',
        'name', 'create_project',
        'description', 'Create a project in the current organization',
        'path', '/projects',
        'httpMethod', '^POST$',
        'access', 'authenticated',
        'successStatus', 201,
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'name',  jsonb_build_object('type', 'string'),
                'dueOn', jsonb_build_object('type', 'string', 'format', 'date')),
            'required', jsonb_build_array('name')),
        'outputSchema', jsonb_build_object('$ref', '#/components/schemas/Project')),
    $body$
DECLARE
    v_b    jsonb := api.content_json((request).content);
    v_bad  jsonb := api.malformed_params(v_b, '{"dueOn": "date"}');
    v_name text := v_b->>'name';
    v_due  date := v_b->>'dueOn' ?> NULL::date;
    v_problems jsonb;
    v_row  core.project;
BEGIN
    IF api.current_organization_id() IS NULL THEN
        RETURN api.problem_response(403, 'Forbidden', 'Open an organization session first',
                                    code => 'pgmi.organization_required');
    END IF;
    IF v_bad IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Entity', 'Some fields are malformed',
                                    invalid_params => v_bad);
    END IF;

    SELECT jsonb_agg(api.invalid_param(field, reason) ORDER BY field) INTO v_problems
    FROM core.fn_project_problems(v_name);
    IF v_problems IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Entity', 'Some values are not accepted',
                                    invalid_params => v_problems);
    END IF;

    v_row := core.fn_create_project(v_name, v_due);
    RETURN api.json_response(201, core.fn_project_json(v_row));
END;
    $body$);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', '62226596-7521-4e93-a360-6430e67432ae',
        'name', 'list_projects',
        'description', 'List the current organization''s projects by name',
        'path', '/projects',
        'httpMethod', '^GET$',
        'access', 'authenticated',
        'querySchema', api.page_query_schema(),
        'outputSchema', api.page_output_schema(jsonb_build_object('$ref', '#/components/schemas/Project'))),
    $body$
DECLARE
    v_page  record := api.pagination_params(api.query_params((request).url));
    v_items jsonb;
    v_total bigint;
BEGIN
    SELECT COALESCE(jsonb_agg(core.fn_project_json(s.pr) ORDER BY (s.pr).name, (s.pr).object_id), '[]'),
           max(s.total)
    INTO v_items, v_total
    FROM (SELECT pr, count(*) OVER () AS total
          FROM core.project pr
          WHERE pr.deleted_at IS NULL
          ORDER BY pr.name, pr.object_id
          LIMIT v_page.o_limit OFFSET v_page.o_offset) s;

    RETURN api.page_response(v_items, COALESCE(v_total, 0), v_page.o_limit, v_page.o_offset);
END;
    $body$);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'c503ec2d-1f5d-4b57-9b56-acd3b9cab6c2',
        'name', 'get_project',
        'description', 'Read one project',
        'path', '/projects/{id:uuid}',
        'httpMethod', '^GET$',
        'access', 'authenticated',
        'outputSchema', jsonb_build_object('$ref', '#/components/schemas/Project')),
    $body$
DECLARE
    v_row core.project;
BEGIN
    SELECT * INTO v_row FROM core.project
    WHERE object_id = ((request).path_params->>'id')::uuid AND deleted_at IS NULL;
    IF NOT FOUND THEN
        RETURN api.problem_response(404, 'Not Found', 'Project not found');
    END IF;
    RETURN api.json_response(200, core.fn_project_json(v_row));
END;
    $body$);
```

The list route reads `limit` and `offset`, which `api.page_query_schema()`
declares, so the gateway refuses a bad value before the body runs. Row-level
security already limits every read to the request's organization: no handler
writes an `organization_id` filter.

## 4. Test

A test file takes no `<pgmi-meta>`. It runs on every deploy, inside a
savepoint that is rolled back, over `api/__test__/_setup.sql`, which signs up
Alice as the owner of Acme Corp.

```sql runnable target=api/__test__/test_project.sql
DO $$
DECLARE
    v_alice text := current_setting('test.alice_subject');
    v_acme  uuid := current_setting('test.acme_org_id')::uuid;
    v_h     extensions.hstore := pg_temp.test_session_headers(v_alice, v_acme);
    v_resp  api.http_response;
    v_body  jsonb;
BEGIN
    v_resp := api.rest_invoke('POST', '/projects', v_h, '{"name": "Website", "dueOn": "2027-03-01"}'::jsonb);
    v_body := api.content_json((v_resp).content);
    IF (v_resp).status_code IS DISTINCT FROM 201 OR v_body->>'name' IS DISTINCT FROM 'Website' THEN
        RAISE EXCEPTION 'POST /projects: want 201 Website, got % %', (v_resp).status_code, v_body;
    END IF;

    v_resp := api.rest_invoke('GET', '/projects/' || (v_body->>'id'), v_h, NULL::bytea);
    IF (v_resp).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'GET /projects/{id}: want 200, got %', (v_resp).status_code;
    END IF;

    v_resp := api.rest_invoke('GET', '/projects?limit=10', v_h, NULL::bytea);
    v_body := api.content_json((v_resp).content);
    IF (v_resp).status_code IS DISTINCT FROM 200 OR (v_body->>'total')::int IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'GET /projects: want 200 with one project, got % %', (v_resp).status_code, v_body;
    END IF;

    v_resp := api.rest_invoke('POST', '/projects', v_h, '{"dueOn": "2027-03-01"}'::jsonb);
    v_body := api.content_json((v_resp).content);
    IF (v_resp).status_code IS DISTINCT FROM 400 OR v_body->>'code' IS DISTINCT FROM 'pgmi.invalid_body' THEN
        RAISE EXCEPTION 'missing name: want 400 pgmi.invalid_body from the gateway, got % %', (v_resp).status_code, v_body;
    END IF;

    v_resp := api.rest_invoke('POST', '/projects', v_h, '{"name": "Website", "dueOn": "next week"}'::jsonb);
    v_body := api.content_json((v_resp).content);
    IF (v_resp).status_code IS DISTINCT FROM 422 OR v_body->'invalid-params'->0->>'name' IS DISTINCT FROM 'dueOn' THEN
        RAISE EXCEPTION 'malformed dueOn: want 422 naming dueOn, got % %', (v_resp).status_code, v_body;
    END IF;
    IF v_body->>'detail' ~* 'PL/pgSQL|SQLSTATE|CONTEXT' THEN
        RAISE EXCEPTION 'problem leaks internals: %', v_body;
    END IF;

    v_resp := api.rest_invoke('POST', '/projects', v_h, '{"name": "  "}'::jsonb);
    v_body := api.content_json((v_resp).content);
    IF (v_resp).status_code IS DISTINCT FROM 422
       OR v_body->'invalid-params' IS DISTINCT FROM '[{"name": "name", "reason": "required"}]'::jsonb THEN
        RAISE EXCEPTION 'blank name: want 422 name required, got % %', (v_resp).status_code, v_body;
    END IF;

    v_resp := api.rest_invoke('GET', '/projects/' || gen_random_uuid(), v_h, NULL::bytea);
    IF (v_resp).status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION 'unknown project: want 404, got %', (v_resp).status_code;
    END IF;

    v_resp := api.rest_invoke('POST', '/projects', pg_temp.test_session_headers(v_alice),
                              '{"name": "Website"}'::jsonb);
    IF (v_resp).status_code IS DISTINCT FROM 403 THEN
        RAISE EXCEPTION 'no organization session: want 403, got %', (v_resp).status_code;
    END IF;

    v_resp := api.rest_invoke('POST', '/projects', ''::extensions.hstore, '{"name": "Website"}'::jsonb);
    IF (v_resp).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'anonymous: want 401, got %', (v_resp).status_code;
    END IF;

    v_resp := api.rest_invoke('POST', '/projects', 'x-login-session=>ls_forged'::extensions.hstore,
                              '{"name": "Website"}'::jsonb);
    IF (v_resp).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'forged session handle: want 401, got %', (v_resp).status_code;
    END IF;
END $$;
```

Every status above is asserted with `IS DISTINCT FROM`: a missing key or a
NULL status then fails the test instead of passing it.
