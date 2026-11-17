/*
<pgmi-meta
    id="9b4a3221-f153-452e-bdb1-e3fb7302cbf5"
    idempotent="true">
  <description>
    Validators behind the api.json_schema and api.xml_schema domains. They run
    before 01-types.sql, whose domain CHECKs call them.
  </description>
  <sortKeys>
    <key>004/000/1</key>
  </sortKeys>
</pgmi-meta>
*/

-- The JSON Schema domain validates top-level keyword shapes at DDL time via
-- api.is_valid_json_schema — this catches malformed schemas before they reach
-- runtime. Not a full JSON Schema validator; does not recurse into subschemas.
-- One function, long because it is one CHECK: a domain takes one expression.

CREATE OR REPLACE FUNCTION api.is_valid_json_schema(p_value jsonb)
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    WITH root AS (SELECT p_value AS v)
    SELECT
        -- Root must be object (real schema), boolean (trivial true/false), or NULL
        (v IS NULL OR jsonb_typeof(v) IN ('object', 'boolean'))

        -- Reject empty objects: inputSchema/outputSchema MUST describe shape
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR v <> '{}'::jsonb)

        -- Object-valued schemas must carry at least one recognised keyword
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR (
             v ? 'type' OR v ? '$ref' OR v ? 'enum' OR v ? 'const'
             OR v ? 'allOf' OR v ? 'anyOf' OR v ? 'oneOf' OR v ? 'not'
             OR v ? 'properties' OR v ? 'items' OR v ? 'additionalProperties'
             OR v ? '$id' OR v ? '$schema' OR v ? '$defs' OR v ? 'definitions'
        ))

        -- String-typed keywords
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR (
             (NOT v ? '$schema'     OR jsonb_typeof(v->'$schema') = 'string')
             AND (NOT v ? '$id'     OR jsonb_typeof(v->'$id') = 'string')
             AND (NOT v ? '$ref'    OR jsonb_typeof(v->'$ref') = 'string')
             AND (NOT v ? 'title'   OR jsonb_typeof(v->'title') = 'string')
             AND (NOT v ? 'description' OR jsonb_typeof(v->'description') = 'string')
             AND (NOT v ? '$comment'    OR jsonb_typeof(v->'$comment') = 'string')
             AND (NOT v ? 'format'      OR jsonb_typeof(v->'format') = 'string')
             AND (NOT v ? 'pattern'     OR jsonb_typeof(v->'pattern') = 'string')
        ))

        -- Object-valued keywords
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR (
             (NOT v ? 'properties'        OR jsonb_typeof(v->'properties') = 'object')
             AND (NOT v ? 'patternProperties' OR jsonb_typeof(v->'patternProperties') = 'object')
             AND (NOT v ? '$defs'         OR jsonb_typeof(v->'$defs') = 'object')
             AND (NOT v ? 'definitions'   OR jsonb_typeof(v->'definitions') = 'object')
             AND (NOT v ? 'dependentSchemas'  OR jsonb_typeof(v->'dependentSchemas') = 'object')
             AND (NOT v ? 'dependentRequired' OR jsonb_typeof(v->'dependentRequired') = 'object')
             AND (NOT v ? 'dependencies'      OR jsonb_typeof(v->'dependencies') = 'object')
        ))

        -- Array-valued keywords with element shape checks
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR (
             (NOT v ? 'enum'        OR jsonb_typeof(v->'enum') = 'array')
             AND (NOT v ? 'allOf'   OR jsonb_typeof(v->'allOf') = 'array')
             AND (NOT v ? 'anyOf'   OR jsonb_typeof(v->'anyOf') = 'array')
             AND (NOT v ? 'oneOf'   OR jsonb_typeof(v->'oneOf') = 'array')
             AND (NOT v ? 'prefixItems' OR jsonb_typeof(v->'prefixItems') = 'array')
             AND (NOT v ? 'required' OR (
                 jsonb_typeof(v->'required') = 'array'
                 AND NOT EXISTS (
                     SELECT 1 FROM jsonb_array_elements(v->'required') e
                     WHERE jsonb_typeof(e) <> 'string'
                 )
             ))
        ))

        -- type keyword: string OR array of strings from the known type set
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR NOT v ? 'type' OR (
             (jsonb_typeof(v->'type') = 'string'
              AND v->>'type' IN ('string','number','integer','boolean','object','array','null'))
             OR (jsonb_typeof(v->'type') = 'array' AND NOT EXISTS (
                 SELECT 1 FROM jsonb_array_elements(v->'type') e
                 WHERE jsonb_typeof(e) <> 'string'
                    OR e->>0 IS NULL
                    OR e::text NOT IN
                       ('"string"','"number"','"integer"','"boolean"','"object"','"array"','"null"')
             ))
        ))

        -- Boolean-or-schema keywords
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR (
             (NOT v ? 'not'                       OR jsonb_typeof(v->'not') IN ('object', 'boolean'))
             AND (NOT v ? 'if'                    OR jsonb_typeof(v->'if') IN ('object', 'boolean'))
             AND (NOT v ? 'then'                  OR jsonb_typeof(v->'then') IN ('object', 'boolean'))
             AND (NOT v ? 'else'                  OR jsonb_typeof(v->'else') IN ('object', 'boolean'))
             AND (NOT v ? 'contains'              OR jsonb_typeof(v->'contains') IN ('object', 'boolean'))
             AND (NOT v ? 'propertyNames'         OR jsonb_typeof(v->'propertyNames') IN ('object', 'boolean'))
             AND (NOT v ? 'additionalProperties'  OR jsonb_typeof(v->'additionalProperties') IN ('object', 'boolean'))
             AND (NOT v ? 'unevaluatedProperties' OR jsonb_typeof(v->'unevaluatedProperties') IN ('object', 'boolean'))
             AND (NOT v ? 'additionalItems'       OR jsonb_typeof(v->'additionalItems') IN ('object', 'boolean'))
             AND (NOT v ? 'unevaluatedItems'      OR jsonb_typeof(v->'unevaluatedItems') IN ('object', 'boolean'))
        ))

        -- items: object | array | boolean
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR NOT v ? 'items'
             OR jsonb_typeof(v->'items') IN ('object', 'array', 'boolean'))
    FROM root;
$$;

COMMENT ON FUNCTION api.is_valid_json_schema(jsonb) IS
    'Structural JSON Schema validator used by the api.json_schema domain. Rejects empty {}, unknown type names, non-string required[] elements, and malformed keyword shapes. Does not recurse into subschemas.';

CREATE OR REPLACE FUNCTION api.is_xml_schema_document(p_value xml)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN p_value IS NOT DOCUMENT THEN false
        ELSE xpath_exists(
            '/xsd:schema',
            p_value,
            ARRAY[ARRAY['xsd', 'http://www.w3.org/2001/XMLSchema']]
        )
    END
$$;

COMMENT ON FUNCTION api.is_xml_schema_document(xml) IS
    'Returns true when the value is an XML document whose root element is xsd:schema in the W3C XML Schema namespace. Shallow shape check, not full XSD validation.';
