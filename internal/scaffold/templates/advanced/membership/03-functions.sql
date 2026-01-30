/*
<pgmi-meta
    id="b1000001-0003-4000-8000-000000000001"
    idempotent="true">
  <description>
    Membership business logic: user provisioning, identity lookup, organization access
  </description>
  <sortKeys>
    <key>005/000/003</key>
  </sortKeys>
</pgmi-meta>
*/

-- ============================================================================
-- Identity Lookup
-- ============================================================================

CREATE OR REPLACE FUNCTION membership.get_identity(p_provider TEXT, p_subject_id TEXT)
RETURNS membership.user_identity
LANGUAGE sql STABLE
AS $$
    SELECT * FROM membership.user_identity
    WHERE idp_provider = p_provider AND idp_subject_id = p_subject_id;
$$;

CREATE OR REPLACE FUNCTION membership.get_user_by_identity(p_provider TEXT, p_subject_id TEXT)
RETURNS membership."user"
LANGUAGE sql STABLE
AS $$
    SELECT u.* FROM membership."user" u
    JOIN membership.user_identity ui ON ui.user_object_id = u.object_id
    WHERE ui.idp_provider = p_provider AND ui.idp_subject_id = p_subject_id;
$$;

CREATE OR REPLACE FUNCTION membership.get_user_identities(p_user_id UUID)
RETURNS SETOF membership.user_identity
LANGUAGE sql STABLE
AS $$
    SELECT * FROM membership.user_identity WHERE user_object_id = p_user_id;
$$;

CREATE OR REPLACE FUNCTION membership.get_user_by_email(p_email TEXT)
RETURNS membership."user"
LANGUAGE sql STABLE
AS $$
    SELECT * FROM membership."user" WHERE email = lower(trim(p_email));
$$;

-- ============================================================================
-- Organization Access
-- ============================================================================

CREATE OR REPLACE FUNCTION membership.get_user_default_organization(p_user_id UUID)
RETURNS membership.organization
LANGUAGE sql STABLE
AS $$
    SELECT o.* FROM membership.organization o
    WHERE o.owner_user_id = p_user_id AND o.is_personal = true AND o.is_active = true
    LIMIT 1;
$$;

CREATE OR REPLACE FUNCTION membership.count_user_owned_organizations(p_user_id UUID)
RETURNS BIGINT
LANGUAGE sql STABLE
AS $$
    SELECT count(*) FROM membership.organization
    WHERE owner_user_id = p_user_id AND is_active = true;
$$;

CREATE OR REPLACE FUNCTION membership.can_create_organization(p_user_id UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE
AS $$
    SELECT membership.count_user_owned_organizations(p_user_id) < 5;
$$;

CREATE OR REPLACE FUNCTION membership.has_organization_access(p_user_id UUID, p_org_id UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE
AS $$
    SELECT EXISTS (
        SELECT 1 FROM membership.organization_member
        WHERE user_id = p_user_id AND organization_id = p_org_id AND status = 'active'
    );
$$;

CREATE OR REPLACE FUNCTION membership.is_organization_owner(p_user_id UUID, p_org_id UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE
AS $$
    SELECT EXISTS (
        SELECT 1 FROM membership.organization
        WHERE object_id = p_org_id AND owner_user_id = p_user_id AND is_active = true
    );
$$;

CREATE OR REPLACE FUNCTION membership.get_member_role(p_user_id UUID, p_org_id UUID)
RETURNS membership.member_role
LANGUAGE sql STABLE
AS $$
    SELECT role FROM membership.organization_member
    WHERE user_id = p_user_id AND organization_id = p_org_id AND status = 'active';
$$;

-- ============================================================================
-- User Provisioning (upsert)
-- ============================================================================

CREATE OR REPLACE FUNCTION api.upsert_user(
    p_provider TEXT,
    p_subject_id TEXT,
    p_email TEXT,
    p_display_name TEXT DEFAULT NULL,
    p_email_verified BOOLEAN DEFAULT false
)
RETURNS UUID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, api, extensions, pg_temp
AS $$
DECLARE
    v_user_id UUID;
    v_org_id UUID;
    v_identity membership.user_identity;
BEGIN
    v_identity := membership.get_identity(p_provider, p_subject_id);

    IF v_identity.object_id IS NOT NULL THEN
        UPDATE membership."user"
        SET display_name = COALESCE(p_display_name, display_name),
            email_verified = email_verified OR p_email_verified,
            updated_at = now()
        WHERE object_id = v_identity.user_object_id;
        RETURN v_identity.user_object_id;
    END IF;

    SELECT u.object_id INTO v_user_id
    FROM membership."user" u
    WHERE u.email = lower(trim(p_email));

    IF v_user_id IS NOT NULL THEN
        INSERT INTO membership.user_identity (user_object_id, idp_provider, idp_subject_id)
        VALUES (v_user_id, p_provider, p_subject_id);

        UPDATE membership."user"
        SET display_name = COALESCE(p_display_name, display_name),
            updated_at = now()
        WHERE object_id = v_user_id;
        RETURN v_user_id;
    END IF;

    INSERT INTO membership."user" (email, display_name, email_verified)
    VALUES (lower(trim(p_email)), p_display_name, p_email_verified)
    RETURNING object_id INTO v_user_id;

    INSERT INTO membership.user_identity (user_object_id, idp_provider, idp_subject_id)
    VALUES (v_user_id, p_provider, p_subject_id);

    INSERT INTO membership.organization (name, slug, owner_user_id, is_personal)
    VALUES (
        'Personal',
        'personal-' || v_user_id::TEXT,
        v_user_id,
        true
    )
    RETURNING object_id INTO v_org_id;

    INSERT INTO membership.organization_member (organization_id, user_id, role, status, joined_at)
    VALUES (v_org_id, v_user_id, 'admin', 'active', now());

    RETURN v_user_id;
END;
$$;

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
    v_customer_role TEXT := pg_temp.pgmi_get_param('database_customer_role');
BEGIN
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA membership TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA membership TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA membership TO %I', v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.upsert_user(TEXT,TEXT,TEXT,TEXT,BOOLEAN) TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.upsert_user(TEXT,TEXT,TEXT,TEXT,BOOLEAN) TO %I', v_customer_role);
END $$;

DO $$ BEGIN RAISE NOTICE '  ✓ membership functions installed'; END $$;
