# API Key Authentication

Machine-to-machine authentication for agents, MCP clients, and CI pipelines. Ships with the advanced template in `membership/08-api-keys.sql`.

## Key format

```text
{prefix}_{key_id}_{secret}
```

- **prefix** — short tenant-configurable label (default `pgmi`), sourced from the `pgmi.api_key_prefix` GUC. Set via `ALTER DATABASE mydb SET pgmi.api_key_prefix = 'myapp'` or `--param api_key_prefix=myapp` on deploy.
- **key_id** — 8-char alphanumeric identifier stored unhashed for O(1) lookup. Never secret on its own.
- **secret** — 32 bytes of random material encoded as URL-safe base64.

Only `SHA-256(full_key)` is persisted. The raw key is returned exactly once, at creation, and never recoverable.

## Lifecycle functions

All mutations flow through SECURITY DEFINER functions so the membership and activity checks cannot be bypassed by direct DML (even from the admin role, which holds SELECT only on `membership.api_key`).

| Function | Purpose |
|----------|---------|
| `membership.create_api_key(user_id, organization_id, display_name, expires_at?, activated_at?)` | Issue a new key. Returns `out_api_key` (show to the user exactly once), `out_key_id`, `out_object_id`. Also inserts a `membership.user_identity` row with `provider='apikey'` so the existing JWT/OIDC auth pipeline resolves the key to the owning user. |
| `membership.validate_api_key(raw_key)` | Validate at request time. Returns `is_valid`, `user_id`, `organization_id`, `key_id`, `reason`. Updates `last_used_at` on success. Hash-safe compare (no short-circuit on partial matches). |
| `membership.disable_api_key(key_id)` / `enable_api_key(key_id)` | Temporarily block / restore. Reversible. |
| `membership.revoke_api_key(key_id)` | Permanent. Deletes the matching `user_identity` row so the key cannot be re-enabled into a working identity. |

## Rejection reasons returned by `validate_api_key`

| `reason` | Meaning |
|----------|---------|
| `malformed key` | Wrong prefix, missing parts, or NULL input. |
| `unknown key` | `key_id` not in the table (or already soft-deleted). |
| `invalid secret` | Key material does not match the stored hash. |
| `key is disabled` / `key is revoked` | Status enforcement. |
| `key not yet active` | `activated_at` is in the future. |
| `key expired` | `expires_at` has passed. |
| `user is inactive` | `membership."user".is_active = false`. |
| `organization is inactive` | `membership.organization.is_active = false`. |

Deactivating a user or organization invalidates every key they own immediately — no per-key revoke required.

## Integrating with the auth pipeline

`membership.create_api_key` inserts a `user_identity` row with `idp_provider='apikey'` and `idp_subject_id = key_id`. The auth gateway extracts `{provider}|{subject_id}` from the validated key and sets the session GUC `auth.idp_subject`:

```sql
-- Gateway / transport layer
SELECT is_valid, user_id, key_id
INTO v_valid, v_user, v_key_id
FROM membership.validate_api_key(:authorization_header);

IF v_valid THEN
    PERFORM set_config('auth.idp_subject', 'apikey|' || v_key_id, true);
    -- api.current_user_id() now resolves to v_user for the rest of the session.
END IF;
```

This means RLS policies keyed on `api.current_user_id()` or `auth.idp_subject` work identically for API-key sessions and interactive JWT/OIDC sessions.

## Security posture

- **Hash-safe compare** — `membership.eq_hash_safe(text, text)` XOR-folds byte-wise so the comparison does not short-circuit on the first differing byte. PL/pgSQL cannot guarantee true constant time, but because it compares SHA-256 hashes, any residual timing leak reveals at most hash-prefix similarity, never raw key bytes. A known `key_id` (public) does not help an attacker binary-search the hash.
- **No admin write path** — `INSERT, UPDATE, DELETE, TRUNCATE` revoked on `membership.api_key` from the admin role. All mutations route through the SECURITY DEFINER functions above.
- **Inactive principal rejection** — checked on every validation, not just at issue time.
- **RLS on `membership.api_key`** — customers see their own organization's keys; admin role has read access for ops triage.

## Operational concerns

- **Rotation**: the only supported path is revoke + create. There is no `rotate_api_key` helper; issuing a fresh key gives the caller full control of the transition window.
- **Audit**: `last_used_at` is updated on every successful validation. Sort descending to find stale keys. Exchange tables (`rest_exchange`, `rpc_exchange`, `mcp_exchange`) log every authenticated request if `autoLog=true` on the handler.
- **Secrets in logs**: pgmi does not log raw keys. Exception paths store `sqlstate=<code> detail=<LEFT(SQLERRM,200)>` in the exchange tables, not raw SQLERRM, so keys embedded in error messages by a misbehaving handler do not leak.

## Related

- `docs/SECURITY.md` — broader authentication model, RLS, and trust-boundary notes.
- `docs/MCP.md` — MCP tool authentication via `p_context->>'user_id'`.
- `internal/scaffold/templates/advanced/membership/08-api-keys.sql` — source of truth.
- `internal/scaffold/templates/advanced/membership/__test__/test_api_keys.sql` — lifecycle, edge-case, expiry, and inactive-principal tests.
