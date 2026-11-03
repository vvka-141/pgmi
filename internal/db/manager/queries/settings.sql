SELECT pg_encoding_to_char(d.encoding), d.datcollate, d.datctype,
       d.datlocprovider::text,
       coalesce(to_jsonb(d) ->> 'datlocale', to_jsonb(d) ->> 'daticulocale', ''),
       coalesce(to_jsonb(d) ->> 'daticurules', ''),
       pg_get_userbyid(d.datdba), d.datconnlimit,
       coalesce((SELECT s.setconfig FROM pg_db_role_setting s
                 WHERE s.setdatabase = d.oid AND s.setrole = 0), '{}'),
       shobj_description(d.oid, 'pg_database'),
       pg_encoding_to_char(t.encoding), t.datcollate, t.datctype,
       t.datlocprovider::text,
       coalesce(to_jsonb(t) ->> 'datlocale', to_jsonb(t) ->> 'daticulocale', ''),
       coalesce(to_jsonb(t) ->> 'daticurules', '')
FROM pg_database d, pg_database t
WHERE d.datname = $1 AND t.datname = 'template1'
