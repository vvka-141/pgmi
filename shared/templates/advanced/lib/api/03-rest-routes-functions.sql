/*
<pgmi-meta
    id="e28caca0-b6a0-4c8c-afb8-cc142e5e7530"
    idempotent="true">
  <description>
    REST route ranking: a path template's specificity (literal segments
    outrank parameters) and its shape. They run before 03-rest-routes.sql,
    whose generated column calls them.
  </description>
  <sortKeys>
    <key>004/002/9</key>
  </sortKeys>
</pgmi-meta>
*/

-- How specific a route is, segment by segment: '1' for a literal segment, '0'
-- for a parameter (/tasks/stats → '11', /tasks/{id} → '10'). Routing prefers
-- the higher value, so a literal segment beats a parameter whatever the
-- registration order.
CREATE OR REPLACE FUNCTION internal.route_specificity(p_canonical_path text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT COALESCE(string_agg(CASE WHEN seg ~ '^\{[^}]*\}$' THEN '0' ELSE '1' END, '' ORDER BY ord), '')
    FROM unnest(string_to_array(ltrim(p_canonical_path, '/'), '/')) WITH ORDINALITY AS t(seg, ord);
$$;

-- A route's canonical path with the parameter names erased
-- (/tasks/{id} → /tasks/{}). Two routes that match one path either differ in
-- shape, and then specificity orders them, or share it; registration refuses
-- the second of one shape with a common method, so ranking is total.
CREATE OR REPLACE FUNCTION internal.route_shape(p_canonical_path text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT regexp_replace(p_canonical_path, '\{[^}]*\}', '{}', 'g');
$$;

DO $$
BEGIN
    IF internal.route_specificity('/tasks/stats') IS DISTINCT FROM '11'
       OR internal.route_specificity('/tasks/{id}') IS DISTINCT FROM '10'
       OR internal.route_specificity('/') IS DISTINCT FROM ''
       OR internal.route_shape('/tasks/{id}/notes/{noteId}') IS DISTINCT FROM '/tasks/{}/notes/{}' THEN
        RAISE EXCEPTION 'route ranking helpers disagree with their examples';
    END IF;
END $$;
