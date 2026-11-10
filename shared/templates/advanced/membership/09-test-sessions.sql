/*
<pgmi-meta
    id="b20b7c41-5b71-4a99-8833-68a0d45ee6bb"
    idempotent="true">
  <description>
    Helpers the tests sign in with, as pg_temp functions: they die with the
    deploy session, so no role can call them later. A login is named
    'issuer|subject' and must exist (membership.fn_provision_login).
      pg_temp.test_session_headers(login, organization, amr) opens a login
        session (and an organization session when an organization is given)
        and returns the request headers that carry their handles; an unknown
        login gets no headers. It signs in outside the test's request context,
        which it keeps.
      pg_temp.test_mcp_context(login, organization, role) approves a
        connection for the login's current membership in the organization, as
        the person would on the web, and returns the MCP context that carries
        its handle; no membership, no handle. Without a role, the agent role
        covering what the person holds there, so the agent can do what they can.
      pg_temp.test_act_as(login, organization, mfa) states a request context
        directly, as a trusted SQL session may; NULL clears it.
      pg_temp.test_login(issuer, subject, email, name, verified) provisions a
        login as a sign-in does (membership.fn_provision_login) and returns
        the person.
      pg_temp.test_person('issuer|subject') and pg_temp.test_organization(slug)
        look up a person and an organization outside any request.
      pg_temp.test_operator(person) makes a person a platform operator, as
        the deploy does.
      pg_temp.test_as_kernel() / pg_temp.test_as_owner() switch the test to
        the kernel role, to read or plant membership rows no request may, and
        back. Tests run as the owner, which runs every handler.
  </description>
  <sortKeys>
    <key>005/000/009</key>
  </sortKeys>
</pgmi-meta>
*/

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

CREATE OR REPLACE FUNCTION pg_temp.test_session_headers(
    p_login        text,
    p_organization uuid DEFAULT NULL,
    p_amr          text[] DEFAULT '{}')
RETURNS extensions.hstore
LANGUAGE plpgsql AS $$
DECLARE
    v_caller  text := current_user;
    v_context text[] := ARRAY[current_setting('auth.principal_id', true), current_setting('auth.organization_id', true),
                             current_setting('auth.mfa', true), current_setting('auth.login_session_id', true),
                             current_setting('auth.organization_session_id', true), current_setting('auth.ctx', true),
                             current_setting('auth.actual_principal_id', true)];
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
    PERFORM set_config('auth.principal_id', coalesce(v_context[1], ''), true),
            set_config('auth.organization_id', coalesce(v_context[2], ''), true),
            set_config('auth.mfa', coalesce(v_context[3], ''), true),
            set_config('auth.login_session_id', coalesce(v_context[4], ''), true),
            set_config('auth.organization_session_id', coalesce(v_context[5], ''), true),
            set_config('auth.ctx', coalesce(v_context[6], ''), true),
            set_config('auth.actual_principal_id', coalesce(v_context[7], ''), true);
    IF v_login IS NULL THEN
        RETURN ''::extensions.hstore;
    END IF;
    RETURN extensions.hstore('x-login-session', v_login)
        || CASE WHEN v_org IS NOT NULL THEN extensions.hstore('x-organization-session', v_org)
                ELSE ''::extensions.hstore END;
END $$;

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

CREATE OR REPLACE FUNCTION pg_temp.test_act_as(
    p_login        text,
    p_organization uuid DEFAULT NULL,
    p_mfa          boolean DEFAULT false)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_person text;
BEGIN
    v_person := pg_temp.test_person(p_login)::text;
    PERFORM set_config('auth.principal_id', coalesce(v_person, ''), true),
            set_config('auth.actual_principal_id', '', true),
            set_config('auth.organization_id', coalesce(p_organization::text, ''), true),
            set_config('auth.mfa', CASE WHEN p_mfa THEN 'on' ELSE '' END, true),
            set_config('auth.login_session_id', '', true),
            set_config('auth.organization_session_id', '', true),
            set_config('auth.ctx', '', true);
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
