SELECT EXISTS (
    SELECT FROM pg_locks l
    JOIN pg_database d ON d.oid = l.database
    WHERE d.datname = $1
      AND l.locktype = 'advisory' AND l.objsubid = 1 AND l.granted
      AND ((l.classid::bigint << 32) | l.objid::bigint) = hashtextextended('pgmi.deploy.' || $1, 0)
)
