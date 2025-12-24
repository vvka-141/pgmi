package pgmi

import (
	"context"
)

// DatabaseManager defines the interface for database management operations.
// Implementations are NOT safe for concurrent use. Create separate instances
// for concurrent operations.
type DatabaseManager interface {
	// Exists checks if a database exists.
	Exists(ctx context.Context, conn DBConnection, dbName string) (bool, error)

	// Create creates a new database.
	Create(ctx context.Context, conn DBConnection, dbName string) error

	// Drop drops the specified database.
	Drop(ctx context.Context, conn DBConnection, dbName string) error

	// TerminateConnections terminates all connections to the specified database.
	// This is typically used before dropping a database to ensure no active connections remain.
	TerminateConnections(ctx context.Context, conn DBConnection, dbName string) error
}
