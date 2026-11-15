SELECT pg_catalog.pg_encoding_to_char(d.encoding) AS encoding, d.datcollate AS lc_collate, d.datctype AS lc_ctype,
       d.datlocprovider::text AS provider,
       coalesce(pg_catalog.to_jsonb(d) OPERATOR(pg_catalog.->>) 'datlocale'::text,
                pg_catalog.to_jsonb(d) OPERATOR(pg_catalog.->>) 'daticulocale'::text, '') AS locale,
       coalesce(pg_catalog.to_jsonb(d) OPERATOR(pg_catalog.->>) 'daticurules'::text, '') AS icu_rules,
       pg_catalog.pg_get_userbyid(d.datdba) AS owner, d.datconnlimit AS connlimit,
       coalesce((SELECT s.setconfig FROM pg_catalog.pg_db_role_setting s
                 WHERE s.setdatabase OPERATOR(pg_catalog.=) d.oid AND s.setrole OPERATOR(pg_catalog.=) 0::pg_catalog.oid), '{}') AS settings,
       pg_catalog.shobj_description(d.oid, 'pg_database') AS comment,
       pg_catalog.pg_encoding_to_char(t.encoding) AS template_encoding, t.datcollate AS template_lc_collate,
       t.datctype AS template_lc_ctype,
       t.datlocprovider::text AS template_provider,
       coalesce(pg_catalog.to_jsonb(t) OPERATOR(pg_catalog.->>) 'datlocale'::text,
                pg_catalog.to_jsonb(t) OPERATOR(pg_catalog.->>) 'daticulocale'::text, '') AS template_locale,
       coalesce(pg_catalog.to_jsonb(t) OPERATOR(pg_catalog.->>) 'daticurules'::text, '') AS template_icu_rules
FROM pg_catalog.pg_database d, pg_catalog.pg_database t
WHERE d.datname OPERATOR(pg_catalog.=) $1 AND t.datname OPERATOR(pg_catalog.=) 'template1'
