/*
<pgmi-meta
    id="4c3ca310-56d5-4b13-8fff-3c5577ff15fc"
    idempotent="false">
  <description>
    Per-organization number sequences: core.organization_counter keeps the
    last number handed out per organization and counter name.
  </description>
  <sortKeys>
    <key>005/000/120</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE core.organization_counter (
    organization_id uuid   NOT NULL,
    counter         text   NOT NULL,
    last_value      bigint NOT NULL,
    PRIMARY KEY (organization_id, counter)
);

COMMENT ON TABLE core.organization_counter IS
    'The last number core.next_organization_number handed out, per organization and counter name.';

SELECT core.apply_org_rls('core.organization_counter', p_has_created_by => false);
