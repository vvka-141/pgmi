/*
<pgmi-meta
    id="b1000001-0001-4000-8000-000000000001"
    idempotent="true">
  <description>
    Membership schema: users, organizations, identities, roles, and seed data
  </description>
  <sortKeys>
    <key>005/000/001</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing membership schema'; END $$;

CREATE SCHEMA IF NOT EXISTS membership;

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
    v_customer_role TEXT := pg_temp.pgmi_get_param('database_customer_role');
BEGIN
    EXECUTE format('GRANT USAGE ON SCHEMA membership TO %I', v_admin_role);
    EXECUTE format('GRANT USAGE ON SCHEMA membership TO %I', v_api_role);
    EXECUTE format('GRANT USAGE ON SCHEMA membership TO %I', v_customer_role);
END $$;

-- ============================================================================
-- ENUMs
-- ============================================================================

DO $$ BEGIN
    CREATE TYPE membership.member_role AS ENUM ('reader', 'contributor', 'admin');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE membership.invitation_status AS ENUM ('pending', 'active', 'declined', 'removed');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- ============================================================================
-- Tables
-- ============================================================================

CREATE TABLE IF NOT EXISTS membership."user" (
    object_id UUID PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    email TEXT NOT NULL,
    display_name TEXT,
    email_verified BOOLEAN NOT NULL DEFAULT false,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_user_email UNIQUE (email)
);

CREATE TABLE IF NOT EXISTS membership.user_identity (
    object_id UUID PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    user_object_id UUID NOT NULL REFERENCES membership."user"(object_id) ON DELETE CASCADE,
    idp_provider TEXT NOT NULL,
    idp_subject_id TEXT NOT NULL,
    linked_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_identity UNIQUE (idp_provider, idp_subject_id)
);

CREATE INDEX IF NOT EXISTS ix_user_identity_user
    ON membership.user_identity(user_object_id);

CREATE TABLE IF NOT EXISTS membership.role (
    object_id UUID PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS membership.user_role (
    user_object_id UUID NOT NULL REFERENCES membership."user"(object_id) ON DELETE CASCADE,
    role_object_id UUID NOT NULL REFERENCES membership.role(object_id) ON DELETE CASCADE,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (user_object_id, role_object_id)
);

CREATE TABLE IF NOT EXISTS membership.organization (
    object_id UUID PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    owner_user_id UUID NOT NULL REFERENCES membership."user"(object_id),
    is_personal BOOLEAN NOT NULL DEFAULT false,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ix_organization_owner
    ON membership.organization(owner_user_id);

CREATE TABLE IF NOT EXISTS membership.organization_member (
    object_id UUID PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES membership.organization(object_id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES membership."user"(object_id) ON DELETE CASCADE,
    role membership.member_role NOT NULL DEFAULT 'reader',
    status membership.invitation_status NOT NULL DEFAULT 'pending',
    invited_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    joined_at TIMESTAMPTZ,

    CONSTRAINT uq_org_member UNIQUE (organization_id, user_id)
);

CREATE INDEX IF NOT EXISTS ix_org_member_user
    ON membership.organization_member(user_id);

-- ============================================================================
-- Seed: superuser role
-- ============================================================================

MERGE INTO membership.role AS target
USING (VALUES ('superuser', 'Full system access')) AS source(name, description)
ON target.name = source.name
WHEN NOT MATCHED THEN
    INSERT (name, description) VALUES (source.name, source.description);

-- ============================================================================
-- Views
-- ============================================================================

CREATE OR REPLACE VIEW membership.vw_user_roles AS
SELECT
    ur.user_object_id,
    r.name AS role_name,
    r.object_id AS role_object_id,
    ur.assigned_at
FROM membership.user_role ur
JOIN membership.role r ON r.object_id = ur.role_object_id;

CREATE OR REPLACE VIEW membership.vw_users AS
SELECT
    u.object_id,
    u.email,
    u.display_name,
    u.email_verified,
    u.is_active,
    u.created_at,
    u.updated_at,
    COALESCE(array_agg(DISTINCT r.name) FILTER (WHERE r.name IS NOT NULL), '{}') AS roles
FROM membership."user" u
LEFT JOIN membership.user_role ur ON ur.user_object_id = u.object_id
LEFT JOIN membership.role r ON r.object_id = ur.role_object_id
GROUP BY u.object_id;

CREATE OR REPLACE VIEW membership.vw_user_identities AS
SELECT
    ui.object_id,
    ui.user_object_id,
    ui.idp_provider,
    ui.idp_subject_id,
    ui.linked_at,
    u.email,
    u.display_name
FROM membership.user_identity ui
JOIN membership."user" u ON u.object_id = ui.user_object_id;

CREATE OR REPLACE VIEW membership.vw_organization_members AS
SELECT
    om.object_id,
    om.organization_id,
    om.user_id,
    om.role,
    om.status,
    om.invited_at,
    om.joined_at,
    u.email,
    u.display_name,
    o.name AS organization_name,
    o.slug AS organization_slug
FROM membership.organization_member om
JOIN membership."user" u ON u.object_id = om.user_id
JOIN membership.organization o ON o.object_id = om.organization_id;

-- ============================================================================
-- Parameterized views (functions returning SETOF)
-- ============================================================================

CREATE OR REPLACE FUNCTION membership.pvw_organization_members(p_org_id UUID)
RETURNS TABLE (
    object_id UUID,
    user_id UUID,
    role membership.member_role,
    status membership.invitation_status,
    email TEXT,
    display_name TEXT,
    joined_at TIMESTAMPTZ
)
LANGUAGE sql STABLE
AS $$
    SELECT om.object_id, om.user_id, om.role, om.status, u.email, u.display_name, om.joined_at
    FROM membership.organization_member om
    JOIN membership."user" u ON u.object_id = om.user_id
    WHERE om.organization_id = p_org_id;
$$;

CREATE OR REPLACE FUNCTION membership.pvw_user_organizations(p_user_id UUID)
RETURNS TABLE (
    organization_id UUID,
    name TEXT,
    slug TEXT,
    role membership.member_role,
    status membership.invitation_status,
    is_personal BOOLEAN
)
LANGUAGE sql STABLE
AS $$
    SELECT o.object_id, o.name, o.slug, om.role, om.status, o.is_personal
    FROM membership.organization_member om
    JOIN membership.organization o ON o.object_id = om.organization_id
    WHERE om.user_id = p_user_id AND om.status = 'active';
$$;

-- ============================================================================
-- Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
    v_customer_role TEXT := pg_temp.pgmi_get_param('database_customer_role');
BEGIN
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA membership TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA membership TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA membership TO %I', v_customer_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA membership TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA membership TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA membership TO %I', v_customer_role);
END $$;

DO $$ BEGIN RAISE NOTICE '  ✓ membership schema tables, views, and permissions installed'; END $$;
