/*
<pgmi-meta
    id="f1a9d67f-2a5c-4658-aa5f-463f255a80f6"
    idempotent="true">
  <description>
    Entity registry: core.register_entity(table, type, title expression) and
    the generated view core.vw_entity, one UNION ALL branch per registered
    table, each read under that table's own row-level security.
  </description>
  <sortKeys>
    <key>005/000/111</key>
  </sortKeys>
</pgmi-meta>
*/

-- Rebuilds core.vw_entity from core.entity_type through core.ensure_view, so
-- an unchanged registry takes no lock.
CREATE OR REPLACE FUNCTION core.refresh_entity_view()
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_branches text;
BEGIN
    SELECT string_agg(format('SELECT %L::text AS entity_type, t.object_id::uuid AS object_id, '
                             't.organization_id::uuid AS organization_id, (%s)::text AS title FROM %s t',
                             e.entity_type, e.title_expression, e.relation),
                      E'\nUNION ALL\n' ORDER BY e.entity_type)
    INTO v_branches
    FROM core.entity_type e;

    PERFORM core.ensure_view('core.vw_entity', format(
        'CREATE OR REPLACE VIEW core.vw_entity WITH (security_invoker = true) AS %s',
        COALESCE(v_branches, 'SELECT NULL::text AS entity_type, NULL::uuid AS object_id, '
                             'NULL::uuid AS organization_id, NULL::text AS title WHERE false')));
END $$;

-- Registers a table with object_id and organization_id columns as an entity
-- type, titled by an expression over its row (alias t), and rebuilds
-- core.vw_entity. Call it from the run-once file that creates the table.
CREATE OR REPLACE FUNCTION core.register_entity(p_table regclass, p_entity_type text, p_title_expression text)
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO core.entity_type (entity_type, relation, title_expression)
    VALUES (p_entity_type, p_table, p_title_expression)
    ON CONFLICT (entity_type) DO UPDATE SET relation = excluded.relation, title_expression = excluded.title_expression;
    PERFORM core.refresh_entity_view();
END $$;

COMMENT ON FUNCTION core.register_entity(regclass, text, text) IS
    'Lists the table''s rows in core.vw_entity as p_entity_type, titled by p_title_expression (SQL over the row, alias t). The table needs object_id and organization_id columns.';

SELECT core.refresh_entity_view();

COMMENT ON VIEW core.vw_entity IS
    'Every row of every registered entity type (core.register_entity), read under its table''s own row-level security.';
COMMENT ON COLUMN core.vw_entity.entity_type IS 'The registered entity type (core.entity_type).';
COMMENT ON COLUMN core.vw_entity.object_id IS 'The row''s object_id in its table.';
COMMENT ON COLUMN core.vw_entity.organization_id IS 'The organization the row belongs to.';
COMMENT ON COLUMN core.vw_entity.title IS 'The row titled by its type''s title expression.';
