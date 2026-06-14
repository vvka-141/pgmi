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

COMMENT ON FUNCTION membership.get_identity(TEXT, TEXT) IS
    'Returns the user_identity row for a provider+subject pair. NULL if not linked.';

CREATE OR REPLACE FUNCTION membership.get_user_by_identity(p_provider TEXT, p_subject_id TEXT)
RETURNS membership."user"
LANGUAGE sql STABLE
AS $$
    SELECT u.* FROM membership."user" u
    JOIN membership.user_identity ui ON ui.user_object_id = u.object_id
    WHERE ui.idp_provider = p_provider AND ui.idp_subject_id = p_subject_id;
$$;

COMMENT ON FUNCTION membership.get_user_by_identity(TEXT, TEXT) IS
    'Resolves a provider+subject pair to the linked user record. NULL if no matching identity.';

CREATE OR REPLACE FUNCTION membership.get_user_identities(p_user_id UUID)
RETURNS SETOF membership.user_identity
LANGUAGE sql STABLE
AS $$
    SELECT * FROM membership.user_identity WHERE user_object_id = p_user_id;
$$;

COMMENT ON FUNCTION membership.get_user_identities(UUID) IS
    'Returns all identity provider links for a user. Empty set if user has no linked identities.';

CREATE OR REPLACE FUNCTION membership.get_user_by_email(p_email TEXT)
RETURNS membership."user"
LANGUAGE sql STABLE
AS $$
    SELECT * FROM membership."user" WHERE email = lower(trim(p_email));
$$;

COMMENT ON FUNCTION membership.get_user_by_email(TEXT) IS
    'Looks up a user by email. Normalizes input (lowercase + trim) before matching.';

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

COMMENT ON FUNCTION membership.get_user_default_organization(UUID) IS
    'Returns the personal organization for a user. Every user gets one on first sign-in.';

CREATE OR REPLACE FUNCTION membership.count_user_owned_organizations(p_user_id UUID)
RETURNS BIGINT
LANGUAGE sql STABLE
AS $$
    SELECT count(*) FROM membership.organization
    WHERE owner_user_id = p_user_id AND is_active = true;
$$;

COMMENT ON FUNCTION membership.count_user_owned_organizations(UUID) IS
    'Counts active organizations owned by a user. Used by can_create_organization for quota checks.';

CREATE OR REPLACE FUNCTION membership.can_create_organization(p_user_id UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE
AS $$
    SELECT membership.count_user_owned_organizations(p_user_id) < 5;
$$;

COMMENT ON FUNCTION membership.can_create_organization(UUID) IS
    'Returns true if the user owns fewer than 5 active organizations. Hardcoded quota — change the limit here.';

CREATE OR REPLACE FUNCTION membership.has_organization_access(p_user_id UUID, p_org_id UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE
AS $$
    SELECT EXISTS (
        SELECT 1 FROM membership.organization_member
        WHERE user_id = p_user_id AND organization_id = p_org_id AND status = 'active'
    );
$$;

COMMENT ON FUNCTION membership.has_organization_access(UUID, UUID) IS
    'Security predicate: true if the user is an active member of the organization.';

CREATE OR REPLACE FUNCTION membership.is_organization_owner(p_user_id UUID, p_org_id UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE
AS $$
    SELECT EXISTS (
        SELECT 1 FROM membership.organization
        WHERE object_id = p_org_id AND owner_user_id = p_user_id AND is_active = true
    );
$$;

COMMENT ON FUNCTION membership.is_organization_owner(UUID, UUID) IS
    'Security predicate: true if the user owns the organization and it is active.';

CREATE OR REPLACE FUNCTION membership.get_member_role(p_user_id UUID, p_org_id UUID)
RETURNS membership.member_role
LANGUAGE sql STABLE
AS $$
    SELECT role FROM membership.organization_member
    WHERE user_id = p_user_id AND organization_id = p_org_id AND status = 'active';
$$;

COMMENT ON FUNCTION membership.get_member_role(UUID, UUID) IS
    'Returns the member_role for a user in an organization. NULL if not an active member.';

-- ============================================================================
-- User Provisioning (upsert)
-- ============================================================================

CREATE OR REPLACE FUNCTION membership.upsert_user(
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
    v_is_new_user BOOLEAN;
BEGIN
    RAISE DEBUG 'upsert_user: provider=%, subject=%', p_provider, p_subject_id;

    SELECT ui.user_object_id INTO v_user_id
    FROM membership.user_identity ui
    WHERE ui.idp_provider = p_provider AND ui.idp_subject_id = p_subject_id;

    IF v_user_id IS NOT NULL THEN
        RAISE DEBUG 'upsert_user: Found existing user %, updating', v_user_id;
        UPDATE membership."user"
        SET display_name = COALESCE(p_display_name, display_name),
            email_verified = email_verified OR p_email_verified,
            updated_at = now()
        WHERE object_id = v_user_id;
        RETURN v_user_id;
    END IF;

    RAISE DEBUG 'upsert_user: No existing identity, creating user';

    INSERT INTO membership."user" (email, display_name, email_verified)
    VALUES (lower(trim(p_email)), p_display_name, p_email_verified)
    ON CONFLICT (email) DO UPDATE SET
        display_name = COALESCE(EXCLUDED.display_name, membership."user".display_name),
        email_verified = membership."user".email_verified OR EXCLUDED.email_verified,
        updated_at = now()
    RETURNING object_id, (xmax = 0) INTO v_user_id, v_is_new_user;

    RAISE DEBUG 'upsert_user: User % (is_new: %)', v_user_id, v_is_new_user;

    INSERT INTO membership.user_identity (user_object_id, idp_provider, idp_subject_id)
    VALUES (v_user_id, p_provider, p_subject_id)
    ON CONFLICT (idp_provider, idp_subject_id) DO NOTHING;

    IF NOT FOUND THEN
        RAISE DEBUG 'upsert_user: Identity conflict, fetching existing';
        SELECT ui.user_object_id INTO v_user_id
        FROM membership.user_identity ui
        WHERE ui.idp_provider = p_provider AND ui.idp_subject_id = p_subject_id;
        RETURN v_user_id;
    END IF;

    IF v_is_new_user THEN
        INSERT INTO membership.organization (name, slug, owner_user_id, is_personal)
        VALUES ('Personal', 'personal-' || v_user_id::TEXT, v_user_id, true)
        RETURNING object_id INTO v_org_id;

        INSERT INTO membership.organization_member (organization_id, user_id, role, status, joined_at)
        VALUES (v_org_id, v_user_id, 'admin', 'active', now());

        RAISE DEBUG 'upsert_user: Created personal org %', v_org_id;
    END IF;

    RETURN v_user_id;
END;
$$;

COMMENT ON FUNCTION membership.upsert_user(TEXT, TEXT, TEXT, TEXT, BOOLEAN) IS
    'JIT user provisioning. Creates user + identity + personal org on first sign-in, updates on subsequent. SECURITY DEFINER — only callable by the gateway auth session setup.';

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA membership TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA membership TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA membership TO %I', v_customer_role);
    -- upsert_user is privileged: only internal.setup_auth_session (SECURITY DEFINER
    -- running as owner) may provision users. Revoking from PUBLIC, api, and customer
    -- closes the identity-overwrite attack path while keeping the gateway flow
    -- functional. PUBLIC has default EXECUTE on new functions unless revoked.
    EXECUTE 'REVOKE EXECUTE ON FUNCTION membership.upsert_user(TEXT,TEXT,TEXT,TEXT,BOOLEAN) FROM PUBLIC';
    EXECUTE format('REVOKE EXECUTE ON FUNCTION membership.upsert_user(TEXT,TEXT,TEXT,TEXT,BOOLEAN) FROM %I', v_api_role);
    EXECUTE format('REVOKE EXECUTE ON FUNCTION membership.upsert_user(TEXT,TEXT,TEXT,TEXT,BOOLEAN) FROM %I', v_customer_role);
END $$;

DO $$ BEGIN RAISE NOTICE '  ✓ membership functions installed'; END $$;
