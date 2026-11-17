WITH me AS (
    SELECT r.rolsuper AS super, r.rolcreaterole AS createrole,
           pg_catalog.current_setting('server_version_num')::int OPERATOR(pg_catalog.>=) 160000 AS pg16
    FROM pg_catalog.pg_roles r
    WHERE r.rolname OPERATOR(pg_catalog.=) current_user
), d AS (
    SELECT d.oid, d.datdba, d.datacl,
           pg_catalog.shobj_description(d.oid, 'pg_database') AS comment,
           me.super OR pg_catalog.pg_has_role(d.datdba, 'USAGE') AS acts_as_owner
    FROM pg_catalog.pg_database d, me
    WHERE d.datname OPERATOR(pg_catalog.=) $1
), opt AS (
    SELECT s.setrole, pg_catalog.split_part(o, '=', 1) AS name,
           CASE WHEN p.context OPERATOR(pg_catalog.=) 'superuser'
                THEN pg_catalog.has_parameter_privilege(pg_catalog.split_part(o, '=', 1), 'SET')
                ELSE true END AS may_set
    FROM d
    JOIN pg_catalog.pg_db_role_setting s ON s.setdatabase OPERATOR(pg_catalog.=) d.oid
    CROSS JOIN LATERAL pg_catalog.unnest(s.setconfig) o
    LEFT JOIN pg_catalog.pg_settings p ON p.name OPERATOR(pg_catalog.=) pg_catalog.lower(pg_catalog.split_part(o, '=', 1))
)
SELECT coalesce(pg_catalog.array_agg(gap ORDER BY ord, gap), '{}') FROM (
    SELECT 1 AS ord, pg_catalog.format('the owner %s (CREATE DATABASE ... OWNER needs SET ROLE %s)',
                                       pg_catalog.pg_get_userbyid(d.datdba), pg_catalog.pg_get_userbyid(d.datdba)) AS gap
    FROM d, me
    WHERE NOT me.super AND NOT pg_catalog.pg_has_role(d.datdba, CASE WHEN me.pg16 THEN 'SET' ELSE 'MEMBER' END)
    UNION ALL
    SELECT 2, pg_catalog.format('setting %s on the database', opt.name)
    FROM opt, d, me
    WHERE opt.setrole OPERATOR(pg_catalog.=) 0::pg_catalog.oid AND NOT me.super AND NOT (d.acts_as_owner AND opt.may_set)
    UNION ALL
    SELECT 3, pg_catalog.format('setting %s of role %s in the database', opt.name, r.rolname)
    FROM opt
    JOIN pg_catalog.pg_roles r ON r.oid OPERATOR(pg_catalog.=) opt.setrole
    CROSS JOIN me
    WHERE NOT me.super
      AND NOT (opt.may_set
               AND (r.rolname OPERATOR(pg_catalog.=) current_user
                    OR (me.createrole AND NOT r.rolsuper
                        AND (NOT me.pg16 OR pg_catalog.pg_has_role(r.oid, 'MEMBER WITH ADMIN OPTION')))))
    UNION ALL
    SELECT 4, 'the comment on the database'
    FROM d WHERE d.comment IS NOT NULL AND NOT d.acts_as_owner
    UNION ALL
    SELECT 5, 'the privileges on the database'
    FROM d WHERE d.datacl IS NOT NULL AND NOT d.acts_as_owner
) g
