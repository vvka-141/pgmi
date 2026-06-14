CREATE TABLE IF NOT EXISTS "user" (
    id SERIAL PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    name TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

COMMENT ON TABLE "user" IS
    'Application user accounts. Email is the natural key used for lookups and deduplication.';
COMMENT ON COLUMN "user".email IS
    'Natural key. Used by upsert_user for conflict resolution.';
COMMENT ON COLUMN "user".name IS
    'Display name. NULL when not provided — not the same as empty string.';
