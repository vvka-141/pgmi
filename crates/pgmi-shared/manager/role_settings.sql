SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object('role', pg_catalog.pg_get_userbyid(s.setrole),
                                                                  'options', pg_catalog.to_jsonb(s.setconfig))
                                     ORDER BY pg_catalog.pg_get_userbyid(s.setrole)), '[]')
FROM pg_catalog.pg_db_role_setting s
JOIN pg_catalog.pg_database d ON d.oid OPERATOR(pg_catalog.=) s.setdatabase
WHERE d.datname OPERATOR(pg_catalog.=) $1 AND s.setrole OPERATOR(pg_catalog.<>) 0::pg_catalog.oid
