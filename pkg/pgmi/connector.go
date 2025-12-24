package pgmi

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connector is a unified interface for establishing database connections.
// Different implementations handle various authentication methods
// (standard credentials, certificates, cloud IAM, etc.).
type Connector interface {
	// Connect establishes a connection pool to the database.
	// The returned pool should be closed by the caller when done.
	Connect(ctx context.Context) (*pgxpool.Pool, error)
}
