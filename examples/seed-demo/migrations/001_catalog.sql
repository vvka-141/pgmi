CREATE TABLE IF NOT EXISTS role (
    role_id       integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key           text NOT NULL UNIQUE,
    description   text NOT NULL,
    deprecated_at timestamptz
);

CREATE TABLE IF NOT EXISTS permission (
    permission_id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key           text NOT NULL UNIQUE,
    description   text NOT NULL,
    deprecated_at timestamptz
);

CREATE TABLE IF NOT EXISTS role_permission (
    role_id       integer NOT NULL REFERENCES role,
    permission_id integer NOT NULL REFERENCES permission,
    PRIMARY KEY (role_id, permission_id)
);
