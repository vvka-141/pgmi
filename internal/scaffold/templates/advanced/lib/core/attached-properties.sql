/*
<pgmi-meta
    id="a7f02000-0001-4000-8000-000000000001"
    idempotent="true">
  <description>
    Attached properties: abstract base and text property type for core entities
  </description>
  <sortKeys>
    <key>003/001</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing attached properties'; END $$;

-- ============================================================================
-- Attached Property Base Table (Abstract)
-- ============================================================================

CREATE TABLE IF NOT EXISTS core.attached_property (
    weakref_object_id uuid NOT NULL,
    attribute_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT is_abstract CHECK (false) NO INHERIT
);

COMMENT ON TABLE core.attached_property IS
    'Abstract base for attached properties. Use inheritance to create specific types (text, int, uuid). The weakref_object_id is a weak reference to core.entity - no FK constraint. Orphans cleaned by core.cleanup_orphan_attached_properties().';

COMMENT ON COLUMN core.attached_property.weakref_object_id IS
    'Weak reference to source entity (core.entity.object_id). No FK enforced - orphans cleaned by garbage collection.';

COMMENT ON COLUMN core.attached_property.attribute_name IS
    'Property name (e.g., description, summary, notes)';

-- ============================================================================
-- Attached Text Property
-- ============================================================================

CREATE TABLE IF NOT EXISTS core.attached_text (
    content_checksum bytea NOT NULL,
    content text NOT NULL,

    PRIMARY KEY (weakref_object_id, attribute_name)
) INHERITS (core.attached_property);

CREATE INDEX IF NOT EXISTS ix_attached_text_checksum
    ON core.attached_text(content_checksum);

COMMENT ON TABLE core.attached_text IS
    'Text properties attached to entities. Used for descriptions, summaries, and other searchable text.';

-- ============================================================================
-- Unified Orphan Cleanup
-- ============================================================================

CREATE OR REPLACE FUNCTION core.cleanup_orphan_attached_properties()
RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    v_count integer;
BEGIN
    WITH deleted AS (
        DELETE FROM core.attached_property ap
        WHERE NOT EXISTS (
            SELECT 1 FROM core.entity e
            WHERE e.object_id = ap.weakref_object_id
        )
        RETURNING 1
    )
    SELECT count(*)::integer INTO v_count FROM deleted;

    RETURN v_count;
END;
$$;

COMMENT ON FUNCTION core.cleanup_orphan_attached_properties IS
    'Removes all orphan attached properties (any type) where source entity no longer exists. Query on parent table affects all child tables.';

-- ============================================================================
-- Helper: Set Attached Text
-- ============================================================================

CREATE OR REPLACE FUNCTION core.set_attached_text(
    p_object_id uuid,
    p_attribute_name text,
    p_content text
) RETURNS void
LANGUAGE sql AS $$
    INSERT INTO core.attached_text (weakref_object_id, attribute_name, content_checksum, content)
    VALUES (
        p_object_id,
        p_attribute_name,
        extensions.digest(convert_to(p_content, 'UTF8'), 'sha256'),
        p_content
    )
    ON CONFLICT (weakref_object_id, attribute_name)
    DO UPDATE SET
        content_checksum = extensions.digest(convert_to(p_content, 'UTF8'), 'sha256'),
        content = p_content;
$$;

COMMENT ON FUNCTION core.set_attached_text IS
    'Upserts a text property on an entity. Uses content checksum for change detection.';

-- ============================================================================
-- Helper: Get Attached Text
-- ============================================================================

CREATE OR REPLACE FUNCTION core.get_attached_text(
    p_object_id uuid,
    p_attribute_name text
) RETURNS text
LANGUAGE sql
STABLE AS $$
    SELECT content FROM core.attached_text
    WHERE weakref_object_id = p_object_id AND attribute_name = p_attribute_name;
$$;

COMMENT ON FUNCTION core.get_attached_text IS
    'Retrieves a text property from an entity. Returns NULL if not found.';

-- ============================================================================
-- Inline Test
-- ============================================================================

DO $$
DECLARE
    v_test_id uuid := gen_random_uuid();
    v_retrieved text;
BEGIN
    PERFORM core.set_attached_text(v_test_id, 'description', 'Test description');
    v_retrieved := core.get_attached_text(v_test_id, 'description');

    IF v_retrieved != 'Test description' THEN
        RAISE EXCEPTION 'set/get_attached_text failed: expected "Test description", got "%"', v_retrieved;
    END IF;

    DELETE FROM core.attached_text WHERE weakref_object_id = v_test_id;
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ core.attached_property - abstract base for attached properties';
    RAISE NOTICE '  ✓ core.attached_text - text properties with checksum';
    RAISE NOTICE '  ✓ core.cleanup_orphan_attached_properties() - unified orphan cleanup';
    RAISE NOTICE '  ✓ core.set_attached_text() - upsert helper';
    RAISE NOTICE '  ✓ core.get_attached_text() - retrieval helper';
END $$;

-- ============================================================================
-- Core Entity Views
-- ============================================================================

DO $$ BEGIN RAISE NOTICE '→ Installing core entity views'; END $$;

-- ============================================================================
-- Power-User Analysis View
-- ============================================================================

CREATE OR REPLACE VIEW core.vw_entity_info AS
SELECT
    e.object_id,
    e.created_at,
    now() - e.created_at AS age,

    e.tableoid::regclass::text AS actual_table,
    split_part(e.tableoid::regclass::text, '.', 1) AS schema_name,
    split_part(e.tableoid::regclass::text, '.', 2) AS table_name,

    EXISTS (
        SELECT 1 FROM pg_inherits i
        WHERE i.inhrelid = e.tableoid
        AND i.inhparent = 'core.managed_entity'::regclass
    ) AS is_managed,

    m.deleted_at,
    m.deleted_at IS NOT NULL AS is_deleted,
    COALESCE(ap_counts.attached_count, 0) AS attached_count

FROM core.entity e
LEFT JOIN core.managed_entity m ON m.object_id = e.object_id
LEFT JOIN LATERAL (
    SELECT count(*)::int AS attached_count
    FROM core.attached_property ap
    WHERE ap.weakref_object_id = e.object_id
) ap_counts ON true;

COMMENT ON VIEW core.vw_entity_info IS
    'Power-user analysis view for all entities. Shows lifecycle age, actual table (via tableoid), managed status, deletion state, and attached properties count.';

-- ============================================================================
-- Statistics View (GROUPING SETS)
-- ============================================================================

CREATE OR REPLACE VIEW core.vw_entity_stats AS
WITH entity_base AS (
    SELECT
        e.object_id,
        e.tableoid::regclass::text AS actual_table,
        split_part(e.tableoid::regclass::text, '.', 1) AS schema_name,
        EXISTS (
            SELECT 1 FROM pg_inherits i
            WHERE i.inhrelid = e.tableoid
            AND i.inhparent = 'core.managed_entity'::regclass
        ) AS is_managed,
        m.deleted_at IS NOT NULL AS is_deleted,
        ap.weakref_object_id IS NOT NULL AS has_attached
    FROM core.entity e
    LEFT JOIN core.managed_entity m ON m.object_id = e.object_id
    LEFT JOIN LATERAL (
        SELECT ap.weakref_object_id
        FROM core.attached_property ap
        WHERE ap.weakref_object_id = e.object_id
        LIMIT 1
    ) ap ON true
)
SELECT
    actual_table,
    schema_name,
    is_managed,
    is_deleted,
    has_attached,
    count(*) AS entity_count,

    GROUPING(actual_table) AS _grp_table,
    GROUPING(schema_name) AS _grp_schema,
    GROUPING(is_managed) AS _grp_managed,
    GROUPING(is_deleted) AS _grp_deleted,
    GROUPING(has_attached) AS _grp_attached
FROM entity_base
GROUP BY GROUPING SETS (
    (),
    (actual_table),
    (schema_name),
    (is_managed),
    (is_deleted),
    (has_attached),
    (schema_name, is_managed),
    (actual_table, is_deleted),
    (is_managed, is_deleted)
);

COMMENT ON VIEW core.vw_entity_stats IS
    'Multi-dimensional entity statistics using GROUPING SETS. Use _grp_* columns to identify aggregation level (1=aggregated, 0=specific value).';

-- ============================================================================
-- Summary Dashboard View (single row)
-- ============================================================================

CREATE OR REPLACE VIEW core.vw_entity_summary AS
WITH
    deleted_ids AS (
        SELECT object_id FROM core.managed_entity WHERE deleted_at IS NOT NULL
    ),
    attached_ids AS (
        SELECT DISTINCT weakref_object_id FROM core.attached_property
    ),
    attached_total AS (
        SELECT count(*)::bigint AS cnt FROM core.attached_property
    )
SELECT
    count(*) AS total_entities,
    count(DISTINCT e.tableoid) AS distinct_types,

    count(*) FILTER (WHERE EXISTS (
        SELECT 1 FROM pg_inherits i
        WHERE i.inhrelid = e.tableoid
        AND i.inhparent = 'core.managed_entity'::regclass
    )) AS managed_entities,

    count(*) FILTER (WHERE e.object_id IN (SELECT object_id FROM deleted_ids)) AS deleted_entities,
    count(*) FILTER (WHERE e.object_id IN (SELECT weakref_object_id FROM attached_ids)) AS entities_with_attached,

    (SELECT cnt FROM attached_total) AS total_attached_properties,

    min(e.created_at) AS oldest_entity_at,
    max(e.created_at) AS newest_entity_at

FROM core.entity e;

COMMENT ON VIEW core.vw_entity_summary IS
    'Single-row dashboard showing entity counts by type, managed status, deletion state, and attached properties.';

-- ============================================================================
-- Attached Properties Statistics View
-- ============================================================================

CREATE OR REPLACE VIEW core.vw_attached_stats AS
WITH attached_base AS (
    SELECT
        e.tableoid::regclass::text AS entity_table,
        split_part(e.tableoid::regclass::text, '.', 1) AS entity_schema,
        ap.tableoid::regclass::text AS property_table,
        ap.attribute_name
    FROM core.attached_property ap
    JOIN core.entity e ON e.object_id = ap.weakref_object_id
)
SELECT
    entity_table,
    entity_schema,
    property_table,
    attribute_name,
    count(*) AS property_count,

    GROUPING(entity_table) AS _grp_entity_table,
    GROUPING(entity_schema) AS _grp_entity_schema,
    GROUPING(property_table) AS _grp_property_table,
    GROUPING(attribute_name) AS _grp_attribute
FROM attached_base
GROUP BY GROUPING SETS (
    (),
    (entity_table),
    (entity_schema),
    (property_table),
    (attribute_name),
    (entity_table, attribute_name),
    (entity_schema, property_table),
    (property_table, attribute_name)
);

COMMENT ON VIEW core.vw_attached_stats IS
    'Multi-dimensional attached properties statistics. Shows property distribution by entity type, property type, and attribute name.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ core.vw_entity_info - power-user analysis view';
    RAISE NOTICE '  ✓ core.vw_entity_stats - multi-dimensional statistics';
    RAISE NOTICE '  ✓ core.vw_entity_summary - dashboard summary';
    RAISE NOTICE '  ✓ core.vw_attached_stats - attached properties statistics';
END $$;

-- ============================================================================
-- Grant Permissions on Views
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON core.vw_entity_info TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON core.vw_entity_stats TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON core.vw_entity_summary TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON core.vw_attached_stats TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON core.vw_entity_info TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON core.vw_entity_stats TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON core.vw_entity_summary TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON core.vw_attached_stats TO %I', v_admin_role);
END $$;
