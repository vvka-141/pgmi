DO $$
DECLARE
    v_count integer;
BEGIN
    SELECT count(*) INTO v_count
    FROM role r
    WHERE r.deprecated_at IS NULL
      AND NOT EXISTS (SELECT 1 FROM role_permission rp
                      WHERE rp.role_id = r.role_id);
    IF v_count > 0 THEN
        RAISE EXCEPTION 'catalog invariant violated: % active role(s) with no permissions', v_count;
    END IF;

    SELECT count(*) INTO v_count FROM permission p
    WHERE NOT EXISTS (SELECT 1 FROM role_permission rp
                      WHERE rp.permission_id = p.permission_id);
    IF v_count > 0 THEN
        RAISE NOTICE '% permission(s) granted to no role (allowed, reported)', v_count;
    END IF;
END $$;
