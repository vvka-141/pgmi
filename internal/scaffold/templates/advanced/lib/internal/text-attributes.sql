/*
<pgmi-meta
    id="a7f02000-0001-4000-8000-000000000001"
    idempotent="true">
  <description>
    Text attributes table for semantic search on handler descriptions
  </description>
  <sortKeys>
    <key>002/001</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing text attributes infrastructure'; END $$;

-- ============================================================================
-- Text Attribute Table (Attached Properties Pattern)
-- ============================================================================
-- Descriptions and other searchable text stored separately for future
-- semantic search via pgvector and FTS. Inspired by WPF attached properties.

CREATE TABLE IF NOT EXISTS internal.text_attribute (
    source_object_id uuid NOT NULL,
    attribute_name text NOT NULL,
    content_checksum bytea NOT NULL,
    content text NOT NULL,

    PRIMARY KEY (source_object_id, attribute_name)
);

CREATE INDEX IF NOT EXISTS ix_text_attribute_checksum
    ON internal.text_attribute(content_checksum);

-- ============================================================================
-- Orphan Cleanup Function
-- ============================================================================
-- Note: Uses dynamic SQL to avoid dependency on api.handler at creation time.
-- Safe to call even if api.handler doesn't exist yet.

CREATE OR REPLACE FUNCTION internal.cleanup_orphan_text_attributes()
RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    v_count integer := 0;
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'api' AND table_name = 'handler'
    ) THEN
        EXECUTE $sql$
            WITH deleted AS (
                DELETE FROM internal.text_attribute ta
                WHERE NOT EXISTS (
                    SELECT 1 FROM api.handler h WHERE h.object_id = ta.source_object_id
                )
                RETURNING 1
            )
            SELECT count(*)::integer FROM deleted
        $sql$ INTO v_count;
    END IF;
    RETURN v_count;
END;
$$;

-- ============================================================================
-- Helper: Set Text Attribute with Checksum
-- ============================================================================

CREATE OR REPLACE FUNCTION internal.set_text_attribute(
    p_object_id uuid,
    p_attribute_name text,
    p_content text
) RETURNS void
LANGUAGE sql AS $$
    INSERT INTO internal.text_attribute (source_object_id, attribute_name, content_checksum, content)
    VALUES (
        p_object_id,
        p_attribute_name,
        extensions.digest(convert_to(p_content, 'UTF8'), 'sha256'),
        p_content
    )
    ON CONFLICT (source_object_id, attribute_name)
    DO UPDATE SET
        content_checksum = extensions.digest(convert_to(p_content, 'UTF8'), 'sha256'),
        content = p_content;
$$;

-- ============================================================================
-- Helper: Get Text Attribute
-- ============================================================================

CREATE OR REPLACE FUNCTION internal.get_text_attribute(
    p_object_id uuid,
    p_attribute_name text
) RETURNS text
LANGUAGE sql
STABLE AS $$
    SELECT content FROM internal.text_attribute
    WHERE source_object_id = p_object_id AND attribute_name = p_attribute_name;
$$;

-- Inline test
DO $$
DECLARE
    v_test_id uuid := gen_random_uuid();
    v_retrieved text;
BEGIN
    PERFORM internal.set_text_attribute(v_test_id, 'description', 'Test description');
    v_retrieved := internal.get_text_attribute(v_test_id, 'description');

    IF v_retrieved != 'Test description' THEN
        RAISE EXCEPTION 'set/get_text_attribute failed: expected "Test description", got "%"', v_retrieved;
    END IF;

    DELETE FROM internal.text_attribute WHERE source_object_id = v_test_id;
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ internal.text_attribute - attached text properties table';
    RAISE NOTICE '  ✓ internal.cleanup_orphan_text_attributes() - orphan cleanup';
    RAISE NOTICE '  ✓ internal.set_text_attribute() - upsert helper';
    RAISE NOTICE '  ✓ internal.get_text_attribute() - retrieval helper';
END $$;
