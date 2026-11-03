# Composite tenant keys, proven

## Contents

- [The fixture](#the-fixture): tables, view, problem function, kernel
- [The test](#the-test): cross-organization links refused, the kernel's refusals

The fixture builds two organizations' tables the way this template writes
them: entity standards, forced row-level security, a composite foreign key, a
view through `core.ensure_view`, a problem function and a kernel. It is
deployed as a test fixture so it rolls back; in a project the tables go in a
run-once file and the rest in re-run files.

## The fixture

```sql runnable target=api/__test__/pgmi_api_layer/_setup.sql
CREATE TABLE core.document (
    object_id       core.entity_id PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    organization_id uuid NOT NULL DEFAULT api.current_organization_id(),
    name            text NOT NULL,
    CONSTRAINT document_organization_object_key UNIQUE (organization_id, object_id)
);
CREATE TABLE core.document_review (
    object_id       core.entity_id PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    organization_id uuid NOT NULL DEFAULT api.current_organization_id(),
    document_id     uuid NOT NULL,
    reviewer_note   text NOT NULL,
    status          text NOT NULL DEFAULT 'open' CONSTRAINT document_review_status_check CHECK (status IN ('open', 'done')),
    CONSTRAINT document_review_document_fkey FOREIGN KEY (organization_id, document_id)
        REFERENCES core.document (organization_id, object_id)
);
CREATE INDEX ix_document_review_document ON core.document_review (organization_id, document_id);
SELECT pg_temp.apply_entity_table_standards('core.document');
SELECT pg_temp.apply_entity_table_standards('core.document_review');
SELECT core.apply_org_rls('core.document', p_has_created_by => false);
SELECT core.apply_org_rls('core.document_review', p_has_created_by => false);

SELECT core.ensure_view('core.vw_document', $view$
    CREATE OR REPLACE VIEW core.vw_document WITH (security_invoker = true) AS
    SELECT p.object_id, p.name,
           (SELECT count(*) FROM core.document_review t
             WHERE t.document_id = p.object_id AND t.status = 'open' AND t.deleted_at IS NULL) AS open_review_count
    FROM core.document p
    WHERE p.deleted_at IS NULL
$view$);

CREATE FUNCTION core.fn_document_problem(p_object_id uuid, p_operation text)
RETURNS text LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN p.object_id IS NULL                                     THEN 'core.document.not_found'
        WHEN p_operation = 'archive' AND p.open_review_count > 0     THEN 'core.document.has_open_reviews'
    END
    FROM (SELECT p_object_id) k
    LEFT JOIN core.vw_document p ON p.object_id = p_object_id;
$$;

CREATE FUNCTION core.fn_create_document(p_name text)
RETURNS core.document LANGUAGE sql AS $$
    INSERT INTO core.document (name) VALUES (btrim(p_name)) RETURNING *;
$$;

CREATE FUNCTION core.fn_archive_document(p_object_id uuid)
RETURNS core.document LANGUAGE sql AS $$
    UPDATE core.document SET deleted_at = now()
    WHERE object_id = p_object_id AND deleted_at IS NULL
      AND core.fn_document_problem(p_object_id, 'archive') IS NULL
    RETURNING *;
$$;
```

## The test

```sql runnable target=api/__test__/pgmi_api_layer/test_pgmi_api_layer.sql
DO $$
DECLARE
    v_alice   text := current_setting('test.alice_subject');
    v_acme    uuid := current_setting('test.acme_org_id')::uuid;
    v_other   uuid;
    v_document core.document;
    v_problem text;
BEGIN
    v_other := (membership.fn_create_organization_with_owner('Other Ltd', 'other-ltd',
                pg_temp.test_person(v_alice))).object_id;

    PERFORM pg_temp.test_act_as(v_alice, v_acme);
    v_document := core.fn_create_document('Handbook');
    IF v_document.object_id IS NULL THEN
        RAISE EXCEPTION 'the kernel must create a valid document';
    END IF;

    INSERT INTO core.document_review (document_id, reviewer_note) VALUES (v_document.object_id, 'Check the dates');
    v_problem := core.fn_document_problem(v_document.object_id, 'archive');
    IF v_problem IS DISTINCT FROM 'core.document.has_open_reviews' OR (core.fn_archive_document(v_document.object_id)).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'archive with open reviews: want core.document.has_open_reviews and no row, got %', v_problem;
    END IF;

    PERFORM pg_temp.test_act_as(v_alice, v_other);
    IF core.fn_document_problem(v_document.object_id, 'archive') IS DISTINCT FROM 'core.document.not_found' THEN
        RAISE EXCEPTION 'another organization must not see the document';
    END IF;
    BEGIN
        INSERT INTO core.document_review (document_id, reviewer_note) VALUES (v_document.object_id, 'Smuggled');
        RAISE EXCEPTION 'a review must not reference another organization''s document';
    EXCEPTION WHEN foreign_key_violation THEN
        NULL;
    END;
    PERFORM pg_temp.test_act_as(NULL);
END $$;
```

The last check is why the key is composite: with `REFERENCES
core.document (object_id)` the smuggled review would insert, because a
foreign-key check does not see row-level security.
