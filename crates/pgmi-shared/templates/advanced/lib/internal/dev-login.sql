/*
<pgmi-meta
    id="f15eed0f-7554-4208-8934-40ee1fab9ad6"
    idempotent="true">
  <description>
    What a database relabelled from dev keeps of dev login: the dev entry
    point dropped, open development sessions revoked, development issuers
    removed where nothing refers to them. deploy/finalize.sql calls it on
    every deploy whose env is not dev.
  </description>
  <sortKeys>
    <key>002/002</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION internal.retire_dev_login()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_issuer text;
BEGIN
    -- A database relabelled from dev keeps what dev files created;
    -- a development issuer someone signed in with stays (foreign key) but is inert:
    -- api.open_login_session accepts only oidc issuers.
    DROP FUNCTION IF EXISTS api.open_dev_login_session(text, text, text, text);
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_kernel_role'));
    UPDATE membership.login_session s
    SET revoked_at = clock_timestamp(), revoke_reason = 'environment relabelled'
    FROM membership.external_identity e
    JOIN membership.identity_issuer i ON i.id = e.issuer_id
    WHERE s.external_identity_object_id = e.object_id
      AND i.kind = 'development' AND s.revoked_at IS NULL;
    EXECUTE format('SET ROLE %I', v_caller);
    FOR v_issuer IN SELECT id FROM membership.identity_issuer WHERE kind = 'development' LOOP
        BEGIN
            DELETE FROM membership.identity_issuer WHERE id = v_issuer;
        EXCEPTION WHEN foreign_key_violation THEN
            NULL;
        END;
    END LOOP;
END $$;
