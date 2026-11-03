SELECT coalesce(jsonb_agg(jsonb_build_object('role', pg_get_userbyid(s.setrole), 'options', to_jsonb(s.setconfig))
                          ORDER BY pg_get_userbyid(s.setrole)), '[]')
FROM pg_db_role_setting s
JOIN pg_database d ON d.oid = s.setdatabase
WHERE d.datname = $1 AND s.setrole <> 0
