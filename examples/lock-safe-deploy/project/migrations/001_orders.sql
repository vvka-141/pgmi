CREATE TABLE IF NOT EXISTS orders (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id bigint NOT NULL,
    amount      numeric(12,2) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO orders (customer_id, amount)
SELECT (g % 5000) + 1, (g % 1000)::numeric
FROM generate_series(1, 100000) g
WHERE NOT EXISTS (SELECT 1 FROM orders);
