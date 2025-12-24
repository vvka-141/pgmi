package params

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

//go:embed unittest.sql
var unittestSQL string

// CreateSchema creates the temporary pgmi schema and utility functions.
// This must be called before loading parameters into the session.
// Must use the provided connection to ensure session-scoped tables are in the same session.
func CreateSchema(ctx context.Context, conn *pgxpool.Conn) error {
	_, err := conn.Exec(ctx, schemaSQL)
	if err != nil {
		return fmt.Errorf("failed to create pgmi schema: %w", err)
	}
	return nil
}

// CreateUnittestSchema creates the temporary pgmi unit test framework.
// This creates pg_temp.pgmi_unittest_* tables, views, sequences, and functions.
// Should be called after CreateSchema() during session preparation.
// Must use the provided connection to ensure session-scoped tables are in the same session.
func CreateUnittestSchema(ctx context.Context, conn *pgxpool.Conn) error {
	_, err := conn.Exec(ctx, unittestSQL)
	if err != nil {
		return fmt.Errorf("failed to create pgmi unittest schema: %w", err)
	}
	return nil
}
