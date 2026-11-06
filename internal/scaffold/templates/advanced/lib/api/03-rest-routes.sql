/*
<pgmi-meta
    id="a7f01000-0003-4000-8000-000000000001"
    idempotent="false">
  <description>
    REST routing: route table for URL-pattern-based HTTP routing
  </description>
  <sortKeys>
    <key>004/003</key>
  </sortKeys>
</pgmi-meta>
*/

-- ============================================================================
-- REST Route Table
-- ============================================================================

CREATE TABLE api.rest_route (
    handler_object_id uuid PRIMARY KEY
        REFERENCES api.handler(object_id) ON DELETE CASCADE,

    sequence_number bigint GENERATED ALWAYS AS IDENTITY,
    address_regexp text NOT NULL,
    method_regexp text NOT NULL DEFAULT '^(GET|POST|PUT|DELETE|PATCH)$',

    route_name text,
    auto_log boolean NOT NULL DEFAULT true,
    canonical_path text NOT NULL DEFAULT '',
    path_params jsonb NOT NULL DEFAULT '[]',
    query_schema api.json_schema,
    success_statuses integer[] NOT NULL DEFAULT '{200}'
        CHECK (cardinality(success_statuses) > 0 AND 200 <= ALL (success_statuses) AND 299 >= ALL (success_statuses)),
    location_template text CHECK (location_template IS NULL OR 201 = ANY (success_statuses)),
    probe_path text NOT NULL DEFAULT '',
    address_prefix text NOT NULL
        GENERATED ALWAYS AS (split_part(canonical_path, '{', 1)) STORED,
    specificity text NOT NULL
        GENERATED ALWAYS AS (internal.route_specificity(canonical_path)) STORED
);

CREATE INDEX ix_rest_route_lookup
    ON api.rest_route(sequence_number DESC);

-- route_name becomes the handler's function name (api.<name>), so two routes
-- cannot share one — the second CREATE OR REPLACE would overwrite the first's
-- body. Partial: an unnamed route (route_name IS NULL) gets a generated function name
-- and cannot collide.
CREATE UNIQUE INDEX uq_rest_route_name
    ON api.rest_route(route_name)
    WHERE route_name IS NOT NULL;

COMMENT ON TABLE api.rest_route IS
    'REST routes, declared as path templates. Among the routes matching a path and method, the most specific wins (specificity DESC: a literal segment beats a parameter); registration refuses two routes of one shape with a common method, so the choice is never left to registration order.';

COMMENT ON COLUMN api.rest_route.sequence_number IS
    'Registration order of the current deploy. A tiebreak after specificity that a valid catalog never needs.';

COMMENT ON COLUMN api.rest_route.specificity IS
    'Per-segment key, 1 for a literal segment and 0 for a parameter (internal.route_specificity). Routing orders by it first.';

COMMENT ON COLUMN api.rest_route.address_prefix IS
    'The literal text before the first parameter. Routing tests it with starts_with before the regex, so a request runs only the regexes that can match.';

COMMENT ON COLUMN api.rest_route.path_params IS
    'The template''s parameters in capture order, [{"name", "type"}] with type uuid, int or text (api.path_template_param_spec). The gateway hands their values to the handler as (request).path_params and answers 400 invalid_path_parameter to a malformed uuid or int.';

COMMENT ON COLUMN api.rest_route.probe_path IS
    'A concrete path the route answers, each parameter filled in. The conformance tests call every route through it.';

COMMENT ON COLUMN api.rest_route.query_schema IS
    'The querySchema metadata key: a JSON Schema object over the query parameters, in the dialect of inputSchema. api.rest_invoke enforces it before the handler runs (internal.query_schema_violations) and the OpenAPI document publishes each property as an in: query parameter.';

COMMENT ON COLUMN api.rest_route.success_statuses IS
    'The successStatus metadata key: the 2xx statuses the operation answers on success (a PUT that creates: 201 and 200), each published in OpenAPI (204 without content). Default {200}.';

COMMENT ON COLUMN api.rest_route.location_template IS
    'The locationTemplate metadata key: the path of the created resource, each {name} a top-level property of the outputSchema (/connections/{connectionId}). api.rest_invoke fills it from the 201 body, percent-encoded, and answers it under the gateway''s mount (api_base_path) as the Location header; OpenAPI publishes the header. Declared only where a GET reads the created resource.';

COMMENT ON COLUMN api.rest_route.canonical_path IS
    'The declared path template with the parameter types removed (/projects/{id:uuid} → /projects/{id}). address_regexp, the OpenAPI path and its parameter names are derived from it.';

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.rest_route TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rest_route TO %I', v_admin_role);
END $$;

SELECT pg_temp.exempt_from_rls('api.rest_route', 'the handler registry: framework metadata, written by registration at deploy');

-- Named JSON Schemas a handler's schemas reference as
-- {"$ref": "#/components/schemas/<name>"}; published under components.schemas.
CREATE TABLE api.schema_component (
    name text PRIMARY KEY CHECK (name ~ '^[A-Za-z][A-Za-z0-9_.-]{0,63}$' AND name <> 'Problem'),
    schema api.json_schema NOT NULL,
    description text
);

-- One row. Every change to what the OpenAPI document is built from moves the
-- generation, so api.catalog_version() recomputes its digest only then.
CREATE TABLE api.catalog_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    generation bigint NOT NULL DEFAULT 0
);
INSERT INTO api.catalog_state DEFAULT VALUES;

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.schema_component, api.catalog_state TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.schema_component TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT, UPDATE ON api.catalog_state TO %I', v_admin_role);
END $$;

SELECT pg_temp.exempt_from_rls('api.schema_component', 'the handler registry: framework metadata, written by registration at deploy');
SELECT pg_temp.exempt_from_rls('api.catalog_state', 'the handler registry: framework metadata, written by registration at deploy');
