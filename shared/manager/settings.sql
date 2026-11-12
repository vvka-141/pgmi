SELECT pg_encoding_to_char(d.encoding) AS encoding, d.datcollate AS lc_collate, d.datctype AS lc_ctype,
       d.datlocprovider::text AS provider,
       coalesce(to_jsonb(d) ->> 'datlocale', to_jsonb(d) ->> 'daticulocale', '') AS locale,
       coalesce(to_jsonb(d) ->> 'daticurules', '') AS icu_rules,
       pg_get_userbyid(d.datdba) AS owner, d.datconnlimit AS connlimit,
       coalesce((SELECT s.setconfig FROM pg_db_role_setting s
                 WHERE s.setdatabase = d.oid AND s.setrole = 0), '{}') AS settings,
       shobj_description(d.oid, 'pg_database') AS comment,
       pg_encoding_to_char(t.encoding) AS template_encoding, t.datcollate AS template_lc_collate,
       t.datctype AS template_lc_ctype,
       t.datlocprovider::text AS template_provider,
       coalesce(to_jsonb(t) ->> 'datlocale', to_jsonb(t) ->> 'daticulocale', '') AS template_locale,
       coalesce(to_jsonb(t) ->> 'daticurules', '') AS template_icu_rules
FROM pg_database d, pg_database t
WHERE d.datname = $1 AND t.datname = 'template1'
