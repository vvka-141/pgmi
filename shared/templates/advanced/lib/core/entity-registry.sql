/*
<pgmi-meta
    id="1a89f09a-3fdd-4631-bc22-d86f2bc314b3"
    idempotent="false">
  <description>
    Entity registry, opt in per table: core.entity_type maps an entity type to
    its table and the expression that titles a row. core.register_entity fills
    it; core.vw_entity lists every registered row. Registers no table here.
  </description>
  <sortKeys>
    <key>005/000/110</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE core.entity_type (
    entity_type      text     PRIMARY KEY CONSTRAINT entity_type_entity_type_check CHECK (entity_type ~ '^[a-z][a-z0-9_]*$'),
    relation         regclass NOT NULL CONSTRAINT entity_type_relation_key UNIQUE,
    title_expression text     NOT NULL CONSTRAINT entity_type_title_expression_check CHECK (btrim(title_expression) <> '')
);

COMMENT ON TABLE core.entity_type IS
    'The registered entity types: each type''s table and the SQL expression over the table''s row (alias t) that titles it. Written by core.register_entity; core.vw_entity is generated from it.';

SELECT pg_temp.exempt_from_rls('core.entity_type', 'describes tables; holds no organization''s rows');
