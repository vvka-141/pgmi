SELECT d.datacl IS NOT NULL, coalesce(g.grantees, '{}'), coalesce(g.privileges, '{}'), coalesce(g.grantable, '{}')
FROM pg_catalog.pg_database d
CROSS JOIN LATERAL (
    SELECT pg_catalog.array_agg(coalesce(pg_catalog.pg_get_userbyid(CASE WHEN a.grantee OPERATOR(pg_catalog.<>) 0::pg_catalog.oid THEN a.grantee END), '')
                                ORDER BY a.grantee, a.privilege_type) AS grantees,
           pg_catalog.array_agg(a.privilege_type ORDER BY a.grantee, a.privilege_type) AS privileges,
           pg_catalog.array_agg(a.is_grantable ORDER BY a.grantee, a.privilege_type) AS grantable
    FROM pg_catalog.aclexplode(d.datacl) a
    WHERE a.grantee OPERATOR(pg_catalog.<>) d.datdba
) g
WHERE d.datname OPERATOR(pg_catalog.=) $1
