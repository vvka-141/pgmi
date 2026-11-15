SELECT EXISTS (
    SELECT FROM pg_catalog.pg_locks l
    JOIN pg_catalog.pg_database d ON d.oid OPERATOR(pg_catalog.=) l.database
    WHERE d.datname OPERATOR(pg_catalog.=) $1
      AND l.locktype OPERATOR(pg_catalog.=) 'advisory' AND l.objsubid OPERATOR(pg_catalog.=) 1 AND l.granted
      AND ((l.classid::bigint OPERATOR(pg_catalog.<<) 32) OPERATOR(pg_catalog.|) l.objid::bigint)
          OPERATOR(pg_catalog.=) pg_catalog.hashtextextended(pg_catalog.concat('pgmi.deploy.', $1), 0)
)
