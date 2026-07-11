CREATE TABLE IF NOT EXISTS audit_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
