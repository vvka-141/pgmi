package params

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

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
