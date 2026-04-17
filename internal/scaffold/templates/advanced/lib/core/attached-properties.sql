/*
<pgmi-meta
    id="a7f02000-0001-4000-8000-000000000001"
    idempotent="true">
  <description>
    Attached properties: abstract base and text property type keyed by weak
    UUID reference. Orphan cleanup dynamically discovers entity tables via the
    core.entity_id domain, so no inheritance registry is required.
  </description>
  <sortKeys>
    <key>003/001</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing attached properties'; END $$;

CREATE TABLE IF NOT EXISTS core.attached_property (
    weakref_object_id uuid NOT NULL,
    attribute_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT is_abstract CHECK (false) NO INHERIT
);

COMMENT ON TABLE core.attached_property IS
    'Abstract base for attached properties. Inherit to create typed property tables. weakref_object_id is an unenforced UUID reference to any entity table with object_id core.entity_id. Orphans are reaped by core.cleanup_orphan_attached_properties().';

COMMENT ON COLUMN core.attached_property.weakref_object_id IS
    'Weak UUID reference to an entity. No FK enforced; orphans cleaned by garbage collection.';

COMMENT ON COLUMN core.attached_property.attribute_name IS
    'Property name (e.g., description, summary, notes).';

CREATE TABLE IF NOT EXISTS core.attached_text (
    content_checksum bytea NOT NULL,
    content text NOT NULL,

    PRIMARY KEY (weakref_object_id, attribute_name)
) INHERITS (core.attached_property);

CREATE INDEX IF NOT EXISTS ix_attached_text_checksum
    ON core.attached_text(content_checksum);

COMMENT ON TABLE core.attached_text IS
    'Text properties attached to entities. Used for descriptions, summaries, and other searchable text.';

CREATE OR REPLACE FUNCTION core.cleanup_orphan_attached_properties()
RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    v_count integer;
    v_sql   text;
BEGIN
    SELECT string_agg(
        format('SELECT object_id FROM %I.%I', n.nspname, c.relname),
        ' UNION ALL '
    )
    INTO v_sql
    FROM pg_attribute a
    JOIN pg_class c ON c.oid = a.attrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE a.attname = 'object_id'
      AND a.atttypid = 'core.entity_id'::regtype
      AND a.attnum > 0
      AND NOT a.attisdropped
      AND c.relkind IN ('r', 'p')
      AND NOT c.relispartition;

    IF v_sql IS NULL THEN
        RETURN 0;
    END IF;

    EXECUTE format(
        'WITH all_entities AS (%s),
         deleted AS (
             DELETE FROM core.attached_property ap
             WHERE NOT EXISTS (
                 SELECT 1 FROM all_entities e WHERE e.object_id = ap.weakref_object_id
             )
             RETURNING 1
         )
         SELECT count(*)::integer FROM deleted',
        v_sql
    ) INTO v_count;

    RETURN v_count;
END;
$$;

COMMENT ON FUNCTION core.cleanup_orphan_attached_properties IS
    'Removes orphan attached properties where the referenced entity no longer exists. Discovers entity tables dynamically via the core.entity_id domain.';

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
    RAISE NOTICE '  ✓ core.cleanup_orphan_attached_properties() - dynamic orphan cleanup';
    RAISE NOTICE '  ✓ core.set_attached_text() - upsert helper';
    RAISE NOTICE '  ✓ core.get_attached_text() - retrieval helper';
END $$;
