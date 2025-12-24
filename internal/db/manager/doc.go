// Package manager provides database management operations for PostgreSQL.
//
// The manager package offers high-level operations for managing PostgreSQL databases:
//   - Checking database existence
//   - Creating new databases
//   - Dropping existing databases
//   - Terminating active connections
//
// All operations use pgx.Identifier.Sanitize() for safe SQL identifier quoting,
// preventing SQL injection attacks while handling edge cases like database names
// with spaces, quotes, or special characters.
//
// # Example Usage
//
//	mgr := manager.New()
//
//	// Check if database exists
//	exists, err := mgr.Exists(ctx, pool, "mydb")
//
//	// Create a new database
//	err = mgr.Create(ctx, pool, "mydb")
//
//	// Drop a database (terminate connections first)
//	err = mgr.TerminateConnections(ctx, pool, "mydb")
//	err = mgr.Drop(ctx, pool, "mydb")
//
// # Thread Safety
//
// Manager is NOT safe for concurrent use. Create separate instances
// for concurrent operations.
package manager
