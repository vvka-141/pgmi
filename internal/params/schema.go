package params

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/shared"
)

var schemaSQL = shared.SessionSchema

// CreateSchema creates the temporary pgmi schema and utility functions.
// This must be called before loading parameters into the session.
// Must use the provided connection to ensure session-scoped tables are in the same session.
func CreateSchema(ctx context.Context, conn *pgxpool.Conn) error {
	_, err := conn.Exec(ctx, schemaSQL)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" {
		cfg := conn.Conn().Config()
		return fmt.Errorf("failed to create pgmi schema: %w; pgmi's session tables need the TEMPORARY privilege: GRANT TEMPORARY ON DATABASE %s TO %s",
			err, pgx.Identifier{cfg.Database}.Sanitize(), pgx.Identifier{cfg.User}.Sanitize())
	}
	if err != nil {
		return fmt.Errorf("failed to create pgmi schema: %w", err)
	}
	return nil
}

// InsertRuntime records the pgmi binary version and the applied session-API
// contract version for pgmi_runtime_view.
func InsertRuntime(ctx context.Context, conn *pgxpool.Conn, pgmiVersion, contractVersion string) error {
	_, err := conn.Exec(ctx,
		`INSERT INTO pg_temp._pgmi_runtime (pgmi_version, contract_version) VALUES ($1, $2)`,
		pgmiVersion, contractVersion)
	if err != nil {
		return fmt.Errorf("failed to record runtime versions: %w", err)
	}
	return nil
}
