/*
<pgmi-meta
    id="725d02dc-8bb5-4839-8350-de16d983c099"
    idempotent="true">
  <description>
    Development only (a dev/ folder deploys with env=dev alone): sign in as a
    login of a development issuer without an identity provider. The local
    gateway calls it where a real transport would call api.open_login_session.
    The sign-in counts as verified and multi-factor (amr ['mfa']), so a local
    platform operator works; everything after it goes through the same
    sessions, membership and permission checks as a real sign-in.
  </description>
  <sortKeys>
    <key>005/004</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION api.open_dev_login_session(p_issuer_id text, p_subject text, p_email text, p_display_name text)
RETURNS text
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT membership.fn_open_login_session(p_issuer_id, p_subject, p_email, p_display_name, true, ARRAY['mfa'])
    WHERE EXISTS (SELECT FROM membership.identity_issuer WHERE id = p_issuer_id AND kind = 'development');
$$;

COMMENT ON FUNCTION api.open_dev_login_session(text, text, text, text) IS
    'Development impersonation: a login-session handle for a development issuer''s login (verified, amr [mfa]); NULL for any other issuer. Deployed only with env=dev.';
