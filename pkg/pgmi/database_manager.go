package pgmi

import (
	"context"
)

// DatabaseManager defines the interface for database management operations.
// Implementations are NOT safe for concurrent use. Create separate instances
// for concurrent operations.
// DatabaseSettings are the CREATE DATABASE properties that must survive a
// drop-and-recreate.
//
// --overwrite is documented as "drop and recreate the target database", and a
// bare CREATE DATABASE inherits the server defaults — so overwriting a
// LATIN1/C database silently produced a UTF8/en_US.utf8 one. Encoding changes
// how bytes are stored; collation changes index ordering and every comparison,
// which is why pgmi's own plan view pins COLLATE "C". Neither is something the
// user asked to change by recreating a database.
type DatabaseSettings struct {
	Encoding string // pg_encoding_to_char(pg_database.encoding)
	Collate  string // pg_database.datcollate
	CType    string // pg_database.datctype

	// Owner is always restorable: DROP DATABASE already requires ownership or
	// superuser, so whoever got far enough to drop it can name the owner again.
	Owner string

	// ConnectionLimit is pg_database.datconnlimit; -1 means unlimited.
	ConnectionLimit int

	// Options are the database-level GUCs from ALTER DATABASE ... SET, as
	// "name=value". Losing a statement_timeout set here removes a safeguard
	// without saying so.
	Options []string

	// Comment is shobj_description, absent when the database has none.
	Comment *string

	// PreserveLocale is set when the encoding or locale differs from the server
	// default, so recreating has to name them — which in turn forces TEMPLATE
	// template0. When false a plain CREATE DATABASE reproduces the database and
	// keeps inheriting whatever a site installed in template1, so naming them
	// anyway would quietly discard it.
	PreserveLocale bool
}

type DatabaseManager interface {
	// Exists checks if a database exists.
	Exists(ctx context.Context, conn DBConnection, dbName string) (bool, error)

	// Settings reports the properties an existing database was created with, so
	// --overwrite can recreate it the same way. Returns nil when the database
	// is absent, or when it matches the server defaults and there is nothing to
	// preserve.
	Settings(ctx context.Context, conn DBConnection, dbName string) (*DatabaseSettings, error)

	// Create creates a new database. A nil settings uses the server defaults,
	// inheriting template1 as PostgreSQL does by default.
	Create(ctx context.Context, conn DBConnection, dbName string, settings *DatabaseSettings) error

	// Drop drops the specified database.
	Drop(ctx context.Context, conn DBConnection, dbName string) error

	// TerminateConnections terminates all connections to the specified database.
	// This is typically used before dropping a database to ensure no active connections remain.
	TerminateConnections(ctx context.Context, conn DBConnection, dbName string) error
}
