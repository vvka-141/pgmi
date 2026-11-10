/*
<pgmi-meta
    id="70b3d5f8-dc00-49c7-ab94-e4a87457050b"
    idempotent="true">
  <description>
    Membership seed, every deploy:
      1. The authorization catalogue from data/authorization.json: the
         permission vocabulary, the roles and their grants, synced to the file
         before any handler registers. Refused: a grant pattern that matches no
         organization permission, and an org.owner that lacks a permission
         org.admin holds.
      2. The OIDC issuers from the oidc_issuers parameter, synced.
      3. Platform operators from the platform_operators parameter, synced: a
         person dropped from the list has their period closed.
  </description>
  <sortKeys>
    <key>005/000/007</key>
  </sortKeys>
</pgmi-meta>
*/

-- Grant patterns: '*' is every organization permission, 'project.*' a prefix.
-- Platform permissions are never granted to a role. A permission the file no
-- longer lists loses its handler declarations first; a handler that still
-- declares it fails its registration later in the deploy.
CREATE OR REPLACE FUNCTION pg_temp.sync_authorization(p_catalogue jsonb)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_permissions jsonb;
    v_roles       jsonb;
    v_grants      jsonb;
    v_bad         text;
BEGIN
    SELECT coalesce(jsonb_agg(jsonb_build_object(
               'id', e->>'id',
               'is_platform', coalesce((e->>'platform')::boolean, false),
               'description', e->>'description')), '[]')
    INTO v_permissions
    FROM jsonb_array_elements(p_catalogue->'permissions') e;

    SELECT coalesce(jsonb_agg(jsonb_build_object(
               'id', e->>'id', 'name', e->>'name', 'sort_order', (e->>'sortOrder')::int)), '[]')
    INTO v_roles
    FROM jsonb_array_elements(p_catalogue->'roles') e;

    SELECT string_agg(DISTINCT (e->>'id') || ' -> ' || pattern, ', ') INTO v_bad
    FROM jsonb_array_elements(p_catalogue->'roles') e
    CROSS JOIN LATERAL jsonb_array_elements_text(e->'grants') pattern
    WHERE NOT EXISTS (SELECT FROM jsonb_to_recordset(v_permissions) p(id text, is_platform boolean)
                      WHERE NOT p.is_platform AND p.id LIKE replace(replace(pattern, '_', '\_'), '*', '%'));
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'data/authorization.json: a role grant matches no organization permission: %', v_bad;
    END IF;

    SELECT coalesce(jsonb_agg(DISTINCT jsonb_build_object('role_id', e->>'id', 'permission_id', p.id)), '[]')
    INTO v_grants
    FROM jsonb_array_elements(p_catalogue->'roles') e
    CROSS JOIN LATERAL jsonb_array_elements_text(e->'grants') pattern
    JOIN jsonb_to_recordset(v_permissions) p(id text, is_platform boolean)
      ON NOT p.is_platform AND p.id LIKE replace(replace(pattern, '_', '\_'), '*', '%');

    SELECT string_agg(a.permission_id, ', ' ORDER BY a.permission_id) INTO v_bad
    FROM jsonb_to_recordset(v_grants) a(role_id text, permission_id text)
    WHERE a.role_id = 'org.admin'
      AND NOT EXISTS (SELECT FROM jsonb_to_recordset(v_grants) o(role_id text, permission_id text)
                      WHERE o.role_id = 'org.owner' AND o.permission_id = a.permission_id);
    IF v_bad IS NOT NULL
       OR NOT EXISTS (SELECT FROM jsonb_to_recordset(v_roles) r(id text) WHERE r.id = 'org.owner')
       OR NOT EXISTS (SELECT FROM jsonb_to_recordset(v_roles) r(id text) WHERE r.id = 'org.admin') THEN
        RAISE EXCEPTION 'data/authorization.json: org.owner and org.admin must exist, and org.owner must hold every permission of org.admin (missing: %)',
            coalesce(v_bad, 'none');
    END IF;

    DELETE FROM membership.role_permission rp
    WHERE NOT EXISTS (SELECT FROM jsonb_to_recordset(v_grants) g(role_id text, permission_id text)
                      WHERE g.role_id = rp.role_id AND g.permission_id = rp.permission_id);
    DELETE FROM api.handler_permission hp
    WHERE NOT EXISTS (SELECT FROM jsonb_to_recordset(v_permissions) p(id text) WHERE p.id = hp.permission_id);
    PERFORM pg_temp.sync_catalog('membership.permission', v_permissions, 'data/authorization.json (permissions)');
    PERFORM pg_temp.sync_catalog('membership.role', v_roles, 'data/authorization.json (roles)');
    PERFORM pg_temp.sync_catalog('membership.role_permission', v_grants, 'data/authorization.json (grants)');
END $$;

DO $seed$
DECLARE
    v_catalogue jsonb;
BEGIN
    SELECT content::jsonb INTO v_catalogue FROM pg_temp.pgmi_source_view WHERE path = './data/authorization.json';
    IF v_catalogue IS NULL THEN
        RAISE EXCEPTION 'data/authorization.json not found';
    END IF;
    PERFORM pg_temp.sync_authorization(v_catalogue);
END $seed$;

-- The OIDC issuers this deployment accepts sign-ins from, synced: an issuer
-- left out of the list is removed, which the foreign key refuses while logins
-- of it exist.
DO $seed$
DECLARE
    v_issuers jsonb;
BEGIN
    BEGIN
        v_issuers := pg_temp.deployment_setting('oidc_issuers')::jsonb;
    EXCEPTION WHEN invalid_text_representation THEN
        RAISE EXCEPTION 'Parameter oidc_issuers is not valid JSON';
    END;
    IF jsonb_typeof(v_issuers) IS DISTINCT FROM 'array'
       OR EXISTS (SELECT FROM jsonb_array_elements(v_issuers) e WHERE jsonb_typeof(e) IS DISTINCT FROM 'string') THEN
        RAISE EXCEPTION 'Parameter oidc_issuers must be a JSON array of issuer URLs, got %', v_issuers;
    END IF;
    INSERT INTO membership.identity_issuer (id, kind)
    SELECT e, 'oidc' FROM jsonb_array_elements_text(v_issuers) e
    ON CONFLICT (id) DO NOTHING;
    IF EXISTS (SELECT FROM jsonb_array_elements_text(v_issuers) e
               JOIN membership.identity_issuer i ON i.id = e WHERE i.kind <> 'oidc') THEN
        RAISE EXCEPTION 'Parameter oidc_issuers names a development issuer';
    END IF;
    BEGIN
        DELETE FROM membership.identity_issuer i
        WHERE i.kind = 'oidc' AND NOT (v_issuers ? i.id);
    EXCEPTION WHEN foreign_key_violation THEN
        RAISE EXCEPTION 'Parameter oidc_issuers leaves out an issuer people still sign in with'
            USING DETAIL = SQLERRM,
                  HINT = 'Keep the issuer in the list, or move those logins to another issuer first.';
    END;
END $seed$;

-- [{"email": "...", "displayName": "...", "issuer": "...", "subject": "..."}],
-- issuer and subject together or neither. The deploy parameter vouches for the
-- address, so a new operator's email counts as verified. A pg_temp function:
-- it dies with the deploy session, so no role can call it later. It runs as
-- the kernel role: the operator table is forced and the provisioning kernels
-- are the kernel's own.
CREATE OR REPLACE FUNCTION pg_temp.sync_platform_operators(p_operators jsonb)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_caller    text := current_user;
    v_at        timestamptz := clock_timestamp();
    v_op        jsonb;
    v_email     text;
    v_principal uuid;
    v_listed    uuid[] := '{}';
    v_ended     uuid;
BEGIN
    IF jsonb_typeof(p_operators) IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION 'platform_operators must be a JSON array, got %', coalesce(jsonb_typeof(p_operators), 'nothing')
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_kernel_role'));

    FOR v_op IN SELECT * FROM jsonb_array_elements(p_operators) LOOP
        v_email := membership.fn_normalize_email(v_op->>'email');
        IF NOT coalesce(membership.fn_is_well_formed_email(v_email), false)
           OR ((v_op->>'issuer') IS NULL) IS DISTINCT FROM ((v_op->>'subject') IS NULL) THEN
            RAISE EXCEPTION 'platform_operators: each entry needs a valid email, and issuer with subject or neither: %', v_op
                USING ERRCODE = 'invalid_parameter_value';
        END IF;
        SELECT ei.user_principal_object_id INTO v_principal
        FROM membership.external_identity ei
        WHERE ei.issuer_id = v_op->>'issuer' AND ei.subject = v_op->>'subject';
        IF v_principal IS NOT NULL THEN
            PERFORM membership.fn_provision_login(v_op->>'issuer', v_op->>'subject', v_email, NULL, true);
            IF NOT EXISTS (SELECT FROM membership.user_principal u
                           WHERE u.principal_object_id = v_principal AND u.email = v_email AND u.is_email_verified) THEN
                RAISE EXCEPTION 'platform operator %: another person holds this address verified, so the login %|% cannot take it',
                    v_email, v_op->>'issuer', v_op->>'subject'
                    USING ERRCODE = 'invalid_parameter_value';
            END IF;
        ELSE
            SELECT u.principal_object_id INTO v_principal
            FROM membership.user_principal u WHERE u.email = v_email AND u.is_email_verified;
            IF v_principal IS NULL THEN
                v_principal := (membership.fn_create_user_principal(v_email, v_op->>'displayName', true)).principal_object_id;
            END IF;
            IF v_op->>'issuer' IS NOT NULL
               AND (membership.fn_provision_login(v_op->>'issuer', v_op->>'subject', v_email, NULL, true)).principal_object_id
                   IS DISTINCT FROM v_principal THEN
                RAISE EXCEPTION 'platform operator %: the login %|% cannot be linked (unknown issuer or malformed subject)',
                    v_email, v_op->>'issuer', v_op->>'subject'
                    USING ERRCODE = 'invalid_parameter_value';
            END IF;
        END IF;
        v_listed := v_listed || v_principal;
        IF NOT EXISTS (SELECT FROM membership.platform_operator po
                       WHERE po.user_principal_object_id = v_principal AND upper_inf(po.valid_during)) THEN
            INSERT INTO membership.platform_operator (user_principal_object_id, valid_during, note)
            VALUES (v_principal, tstzrange(v_at, NULL, '[)'), 'granted at deploy from platform_operators');
            PERFORM membership.fn_record_audit_event('membership.platform_operator.granted', NULL, v_principal);
        END IF;
    END LOOP;

    FOR v_ended IN
        UPDATE membership.platform_operator po
        SET valid_during = tstzrange(lower(po.valid_during), v_at, '[)')
        WHERE upper_inf(po.valid_during) AND po.user_principal_object_id <> ALL (v_listed)
        RETURNING po.user_principal_object_id
    LOOP
        PERFORM membership.fn_record_audit_event('membership.platform_operator.ended', NULL, v_ended);
    END LOOP;
    EXECUTE format('SET ROLE %I', v_caller);
END $$;

DO $seed$
DECLARE
    v_operators jsonb;
BEGIN
    BEGIN
        v_operators := pg_temp.deployment_setting('platform_operators')::jsonb;
    EXCEPTION WHEN invalid_text_representation THEN
        RAISE EXCEPTION 'Parameter platform_operators is not valid JSON' USING ERRCODE = 'invalid_parameter_value';
    END;
    PERFORM pg_temp.sync_platform_operators(v_operators);
    RAISE NOTICE '[pgmi] Platform operators: %', jsonb_array_length(v_operators);
END $seed$;
