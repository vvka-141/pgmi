/*
<pgmi-meta
    id="ac23b704-b4a1-4d61-8c34-45b9fd946f76"
    idempotent="false">
  <description>
    The environment this database was deployed as. deploy/parameters.sql refuses a
    redeploy whose env differs, unless relabel_environment names the new label.
  </description>
  <sortKeys>
    <key>002/000</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE internal.environment (
    singleton   boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
    label       text        NOT NULL CHECK (label IN ('dev', 'test', 'prod')),
    labelled_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE internal.environment IS
    'One row: dev, test or prod, from the env parameter of the first deploy. Changed only by a deploy with relabel_environment.';

INSERT INTO internal.environment (label) VALUES (current_setting('pgmi.env'));

SELECT pg_temp.exempt_from_rls('internal.environment', 'deploy bookkeeping: written and read by deploy.sql only');
