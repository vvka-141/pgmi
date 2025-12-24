package pgmi

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FileLoader defines the interface for loading file metadata and parameters
// into PostgreSQL session-scoped temporary tables.
// Implementations must be safe for concurrent use by multiple goroutines.
type FileLoader interface {
	// LoadFilesIntoSession creates the pg_temp.pgmi_source table and loads file metadata.
	// Must use the provided connection to ensure session-scoped tables are in the same session.
	LoadFilesIntoSession(ctx context.Context, conn *pgxpool.Conn, files []FileMetadata) error

	// LoadParametersIntoSession creates the pg_temp.pgmi_parameter table and loads parameters.
	// Must use the provided connection to ensure session-scoped tables are in the same session.
	LoadParametersIntoSession(ctx context.Context, conn *pgxpool.Conn, params map[string]string) error
}
