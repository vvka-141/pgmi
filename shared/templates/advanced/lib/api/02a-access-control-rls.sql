/*
<pgmi-meta
    id="58452bcf-8d8b-45bc-8808-bc17ba04ac13"
    idempotent="false">
  <description>
    Declared access, structure, part 3: the refusal log holds organization ids,
    so it is forced to row-level security with one policy for the owner, which
    the gateways write it as and the admin login reads it through. Any other
    role granted SELECT on it, such as a managed provider's reader, sees no
    rows.
  </description>
  <sortKeys>
    <key>004/002/001/000/001</key>
  </sortKeys>
</pgmi-meta>
*/

ALTER TABLE api.access_denial ENABLE ROW LEVEL SECURITY;
ALTER TABLE api.access_denial FORCE ROW LEVEL SECURITY;

DO $$
BEGIN
    EXECUTE format('CREATE POLICY access_denial_owner ON api.access_denial TO %I USING (true) WITH CHECK (true)',
                   pg_temp.deployment_setting('database_owner_role'));
END $$;

DELETE FROM internal.rls_exemption WHERE relation = 'api.access_denial'::regclass;
