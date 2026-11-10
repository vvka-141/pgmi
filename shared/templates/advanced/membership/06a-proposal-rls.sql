/*
<pgmi-meta
    id="34d5744f-3480-4ae7-9150-a6ad692a0eeb"
    idempotent="false">
  <description>
    Row-level security on proposals: a request reads its organization's
    proposals. No request writes them; the proposal kernels do, and pass by
    the kernel policy.
  </description>
  <sortKeys>
    <key>005/000/006/001</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE POLICY proposal_visible ON membership.proposal FOR SELECT
    USING (organization_object_id = (SELECT api.current_organization_id()));
