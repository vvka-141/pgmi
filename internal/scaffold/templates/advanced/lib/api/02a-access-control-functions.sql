/*
<pgmi-meta
    id="2f8d4c71-9a3e-4b06-8c15-e7b0a9d3f6a2"
    idempotent="true">
  <description>
    Declared access, code: the permission hook the application replaces, the one
    access decision every gateway and listing calls, the refusal log writer and
    its retention purge.
  </description>
  <sortKeys>
    <key>004/002/001/001</key>
  </sortKeys>
</pgmi-meta>
*/

-- The framework needs one fact about a permission: does it exist, and is it a
-- platform permission (one that needs no organization). The application owns
-- the vocabulary and replaces this body in a file sorted after this one; this
-- default knows no permissions, so every declared permission is unknown.
CREATE OR REPLACE FUNCTION internal.is_platform_permission(p_permission_id text)
RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT NULL::boolean;
$$;

COMMENT ON FUNCTION internal.is_platform_permission(text) IS
    'Hook the application replaces: whether the permission is a platform permission, NULL when it does not exist. Read by registration (an unknown permission fails the deploy) and by internal.access_denial_reason.';

-- NULL when the call is allowed, else the reason it is refused:
--   unauthenticated        no resolved principal, and the handler is not public
--   organization_required  no organization, and an organization permission could open it
--   permission_missing     no declared permission is held
-- PL/pgSQL so its plans are cached per connection: it runs on every request.
CREATE OR REPLACE FUNCTION internal.access_denial_reason(
    p_handler       uuid,
    p_access        text,
    p_authenticated boolean,
    p_has_org       boolean,
    p_permissions   text[]
) RETURNS text
LANGUAGE plpgsql STABLE
SET search_path = api, internal, common, pg_temp
AS $$
BEGIN
    IF p_access = 'public' THEN
        RETURN NULL;
    END IF;
    IF NOT COALESCE(p_authenticated, false) THEN
        RETURN 'unauthenticated';
    END IF;
    IF p_access = 'authenticated' THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT FROM api.handler_permission hp
               WHERE hp.handler_object_id = p_handler AND hp.permission_id = ANY (p_permissions)) THEN
        RETURN NULL;
    END IF;
    IF NOT COALESCE(p_has_org, false) AND EXISTS (
        SELECT FROM api.handler_permission hp
        WHERE hp.handler_object_id = p_handler
          AND NOT internal.is_platform_permission(hp.permission_id)) THEN
        RETURN 'organization_required';
    END IF;
    RETURN 'permission_missing';
END $$;

COMMENT ON FUNCTION internal.access_denial_reason(uuid, text, boolean, boolean, text[]) IS
    'The access decision for one handler and one principal: NULL when allowed, else unauthenticated | organization_required | permission_missing.';

-- The decision for the current request. The application supplies the readers
-- (api.current_principal_id, api.current_organization_id, api.current_permissions).
CREATE OR REPLACE FUNCTION internal.current_access_denial(p_handler uuid, p_access text)
RETURNS text
LANGUAGE plpgsql STABLE
SET search_path = api, internal, common, pg_temp
AS $$
BEGIN
    IF p_access = 'public' THEN
        RETURN NULL;
    END IF;
    RETURN internal.access_denial_reason(
        p_handler, p_access,
        api.current_principal_id() IS NOT NULL,
        api.current_organization_id() IS NOT NULL,
        api.current_permissions());
END $$;

COMMENT ON FUNCTION internal.current_access_denial(uuid, text) IS
    'internal.access_denial_reason for the current request''s principal, organization and permissions. Called by the REST, RPC and MCP gateways and the MCP listings, so what a caller sees listed is what it may call.';

-- Runs in the request's transaction. A READ ONLY transaction cannot write, so
-- there the refusal goes to the server log.
CREATE OR REPLACE FUNCTION internal.log_access_denial(
    p_handler uuid,
    p_reason  text,
    p_method  text,
    p_path    text
) RETURNS void
LANGUAGE plpgsql
SET search_path = api, internal, common, pg_temp
AS $$
DECLARE
    v_actual    uuid := api.current_actual_principal_id();
    v_effective uuid := api.current_principal_id();
    v_org       uuid := api.current_organization_id();
    v_kind      text := api.current_actor_kind();
BEGIN
    IF internal.transaction_is_read_only() THEN
        RAISE LOG 'access denied: % % handler=% reason=% principal=% effective=% actor=% organization=%',
            p_method, p_path, p_handler, p_reason, v_actual, v_effective, v_kind, v_org;
        RETURN;
    END IF;

    INSERT INTO api.access_denial AS d (
        bucket, handler_object_id, reason, actual_principal_object_id, effective_principal_object_id,
        actor_kind, organization_object_id, last_method, last_path)
    VALUES (date_trunc('minute', now()), p_handler, p_reason, v_actual, v_effective,
            v_kind, v_org, p_method, left(p_path, 500))
    ON CONFLICT (bucket, handler_object_id, reason, actual_principal_object_id,
                 effective_principal_object_id, organization_object_id)
    DO UPDATE SET attempts    = d.attempts + 1,
                  last_at     = now(),
                  last_method = EXCLUDED.last_method,
                  last_path   = EXCLUDED.last_path,
                  actor_kind  = EXCLUDED.actor_kind;
END $$;

CREATE OR REPLACE FUNCTION api.purge_access_denials(p_older_than interval DEFAULT interval '90 days')
RETURNS bigint
LANGUAGE sql AS $$
    WITH gone AS (
        DELETE FROM api.access_denial WHERE bucket < now() - p_older_than RETURNING 1
    )
    SELECT count(*) FROM gone;
$$;

COMMENT ON FUNCTION api.purge_access_denials(interval) IS
    'Retention for api.access_denial (default 90 days). Returns the number of rows removed.';
