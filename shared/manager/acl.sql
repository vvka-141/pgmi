SELECT d.datacl IS NOT NULL, coalesce(g.grantees, '{}'), coalesce(g.privileges, '{}'), coalesce(g.grantable, '{}')
FROM pg_database d
CROSS JOIN LATERAL (
    SELECT array_agg(coalesce(pg_get_userbyid(nullif(a.grantee, 0)), '') ORDER BY a.grantee, a.privilege_type) AS grantees,
           array_agg(a.privilege_type ORDER BY a.grantee, a.privilege_type) AS privileges,
           array_agg(a.is_grantable ORDER BY a.grantee, a.privilege_type) AS grantable
    FROM aclexplode(d.datacl) a
    WHERE a.grantee <> d.datdba
) g
WHERE d.datname = $1
