-- ============================================================================
-- Library test fixture: the identity issuers the tests' people sign in from.
-- ============================================================================

INSERT INTO membership.identity_issuer (id, kind)
VALUES ('test', 'development'), ('google', 'development'), ('github', 'development'), ('samlp', 'development')
ON CONFLICT (id) DO NOTHING;
