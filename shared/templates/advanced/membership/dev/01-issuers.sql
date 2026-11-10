/*
<pgmi-meta
    id="da8662f0-b567-4166-948b-39d1d5453408"
    idempotent="true">
  <description>
    Development only (a dev/ folder deploys with env=dev alone): the identity
    issuer 'dev', whose logins a local gateway may present without a real
    identity provider. Sorted before the membership seed, so a platform
    operator may be listed with a dev login.
  </description>
  <sortKeys>
    <key>005/000/006/001</key>
  </sortKeys>
</pgmi-meta>
*/

INSERT INTO membership.identity_issuer (id, kind) VALUES ('dev', 'development')
ON CONFLICT (id) DO NOTHING;
