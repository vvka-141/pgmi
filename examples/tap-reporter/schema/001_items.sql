CREATE TABLE IF NOT EXISTS item (
    id   SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    qty  INT  NOT NULL DEFAULT 0 CHECK (qty >= 0)
);

CREATE OR REPLACE FUNCTION upsert_item(p_name TEXT, p_qty INT DEFAULT 0)
RETURNS item
LANGUAGE SQL AS $$
    INSERT INTO item (name, qty) VALUES (p_name, p_qty)
    ON CONFLICT (name) DO UPDATE SET qty = EXCLUDED.qty
    RETURNING *;
$$;
