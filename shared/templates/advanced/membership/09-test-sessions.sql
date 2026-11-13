/*
<pgmi-meta
    id="b20b7c41-5b71-4a99-8833-68a0d45ee6bb"
    idempotent="true">
  <description>
    Helpers the tests sign in with, as pg_temp functions: they die with the
    deploy session, so no role can call them later. A login is named
    'issuer|subject' and must exist (membership.fn_provision_login). Tests
    run as the owner, which runs every handler.
  </description>
  <sortKeys>
    <key>005/000/009</key>
  </sortKeys>
</pgmi-meta>
*/

-- Switch the test to the kernel role, to read or plant membership rows no
-- request may, and back to the owner.
CREATE OR REPLACE FUNCTION pg_temp.test_as_kernel()
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_kernel_role'));
END $$;

CREATE OR REPLACE FUNCTION pg_temp.test_as_owner()
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_owner_role'));
END $$;

-- Provisions a login as a sign-in does and returns the person.
CREATE OR REPLACE FUNCTION pg_temp.test_login(
    p_issuer      text,
    p_subject     text,
    p_email       text,
    p_name        text DEFAULT NULL,
    p_is_verified boolean DEFAULT true)
RETURNS uuid
LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_person uuid;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    v_person := (membership.fn_provision_login(p_issuer, p_subject, p_email, p_name, p_is_verified)).principal_object_id;
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_person;
END $$;

-- A person and an organization, looked up outside any request.
CREATE OR REPLACE FUNCTION pg_temp.test_person(p_login text)
RETURNS uuid
LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_person uuid;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    SELECT ei.user_principal_object_id INTO v_person FROM membership.external_identity ei
    WHERE ei.issuer_id = split_part(p_login, '|', 1)
      AND ei.subject = substr(p_login, strpos(p_login, '|') + 1);
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_person;
END $$;

CREATE OR REPLACE FUNCTION pg_temp.test_organization(p_slug text)
RETURNS uuid
LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_org    uuid;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    SELECT o.object_id INTO v_org FROM membership.organization o WHERE o.slug = p_slug;
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_org;
END $$;

-- Makes a person a platform operator, as the deploy does.
CREATE OR REPLACE FUNCTION pg_temp.test_operator(p_person uuid)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    INSERT INTO membership.platform_operator (user_principal_object_id, valid_during, note)
    VALUES (p_person, tstzrange(clock_timestamp(), NULL, '[)'), 'test fixture');
    EXECUTE format('SET ROLE %I', v_caller);
END $$;

-- Opens a login session (and an organization session when an organization
-- is given) and returns the request headers that carry their handles; an
-- unknown login gets no headers. It signs in outside the test's request
-- context, which it keeps.
CREATE OR REPLACE FUNCTION pg_temp.test_session_headers(
    p_login        text,
    p_organization uuid DEFAULT NULL,
    p_amr          text[] DEFAULT '{}')
RETURNS extensions.hstore
LANGUAGE plpgsql AS $$
DECLARE
    v_caller  text := current_user;
    v_settings text[] := membership.fn_request_input_settings() || 'auth.ctx'::text;
    v_context  text[] := ARRAY(SELECT current_setting(s, true) FROM unnest(v_settings) s);
    v_login   text;
    v_session uuid;
    v_org     text;
BEGIN
    PERFORM pg_temp.test_act_as(NULL);
    PERFORM pg_temp.test_as_kernel();
    v_login := membership.fn_start_login_session(
        (SELECT ei.object_id FROM membership.external_identity ei
          WHERE ei.issuer_id = split_part(p_login, '|', 1)
            AND ei.subject = substr(p_login, strpos(p_login, '|') + 1)),
        p_amr);
    IF v_login IS NOT NULL AND p_organization IS NOT NULL THEN
        SELECT l.object_id INTO v_session FROM membership.login_session l
        WHERE l.token_hash = membership.fn_session_token_hash(v_login);
        v_org := (membership.fn_open_organization_session(v_session, p_organization)).token;
    END IF;
    EXECUTE format('SET ROLE %I', v_caller);
    PERFORM set_config(c.setting, coalesce(c.value, ''), true)
    FROM unnest(v_settings, v_context) AS c(setting, value);
    IF v_login IS NULL THEN
        RETURN ''::extensions.hstore;
    END IF;
    RETURN extensions.hstore('x-login-session', v_login)
        || CASE WHEN v_org IS NOT NULL THEN extensions.hstore('x-organization-session', v_org)
                ELSE ''::extensions.hstore END;
END $$;

-- Approves a connection for the login's current membership in the
-- organization, as the person would on the web, and returns the MCP context
-- that carries its handle; no membership, no handle. Without a role, the
-- agent role covering what the person holds there, so the agent can do what
-- they can.
CREATE OR REPLACE FUNCTION pg_temp.test_mcp_context(
    p_login        text,
    p_organization uuid,
    p_role         text DEFAULT NULL)
RETURNS jsonb
LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_token  text;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    SELECT s.token INTO v_token
    FROM membership.external_identity ei
    JOIN membership.organization_membership m ON m.principal_object_id = ei.user_principal_object_id
                                             AND m.organization_object_id = p_organization
                                             AND upper_inf(m.valid_during)
    CROSS JOIN LATERAL membership.fn_start_connection(m.object_id,
        coalesce(p_role, (
            SELECT r.id FROM membership.role r
            WHERE r.id LIKE 'agent.%'
              AND NOT EXISTS (SELECT unnest(membership.fn_effective_permissions(m.principal_object_id, m.organization_object_id, false))
                              EXCEPT SELECT rp.permission_id FROM membership.role_permission rp WHERE rp.role_id = r.id)
            ORDER BY (SELECT count(*) FROM membership.role_permission rp WHERE rp.role_id = r.id), r.sort_order, r.id
            LIMIT 1)),
        'test client', 1) s
    WHERE ei.issuer_id = split_part(p_login, '|', 1)
      AND ei.subject = substr(p_login, strpos(p_login, '|') + 1);
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN CASE WHEN v_token IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('connection', v_token) END;
END $$;

-- States a request context directly, as a trusted SQL session may; NULL
-- clears it.
CREATE OR REPLACE FUNCTION pg_temp.test_act_as(
    p_login        text,
    p_organization uuid DEFAULT NULL,
    p_mfa          boolean DEFAULT false)
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM membership.fn_set_request_inputs(
        p_principal_id            => pg_temp.test_person(p_login),
        p_actual_principal_id     => NULL,
        p_organization_id         => p_organization,
        p_mfa                     => p_mfa,
        p_login_session_id        => NULL,
        p_organization_session_id => NULL);
END $$;

DO $$
BEGIN
    EXECUTE format('GRANT EXECUTE ON FUNCTION pg_temp.test_as_kernel(), pg_temp.test_as_owner(), '
                   'pg_temp.test_login(text, text, text, text, boolean), pg_temp.test_operator(uuid), '
                   'pg_temp.test_person(text), pg_temp.test_organization(text), '
                   'pg_temp.test_session_headers(text, uuid, text[]), pg_temp.test_mcp_context(text, uuid, text), '
                   'pg_temp.test_act_as(text, uuid, boolean) TO %I',
                   pg_temp.deployment_setting('database_kernel_role'));
END $$;
