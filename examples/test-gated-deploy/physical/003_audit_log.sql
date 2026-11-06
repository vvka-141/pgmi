/*
<pgmi-meta id="b09e5249-53ef-41ba-9586-3a9c64b8da7a" idempotent="false">
  <description>Deploy audit log</description>
</pgmi-meta>
*/
CREATE TABLE audit_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
