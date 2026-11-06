package services

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/session"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// FileScanner discovers a project's files. Implementations must be safe for
// concurrent use.
type FileScanner interface {
	// ScanDirectory excludes deploy.sql, the orchestrator script.
	ScanDirectory(sourcePath string) (scanner.Result, error)
	ValidateDeploySQL(sourcePath string) error
	ReadDeploySQL(sourcePath string) (string, error)
}

// FileLoader fills the session tables. Each method must use conn, so the
// session-scoped tables land in the deploy's session. Implementations must be
// safe for concurrent use.
type FileLoader interface {
	LoadFilesIntoSession(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error
	LoadParametersIntoSession(ctx context.Context, conn *pgxpool.Conn, params map[string]string) error
}

// SessionPreparer abstracts session preparation for testability.
//
// ScanProject is separate from PrepareSession so a caller can validate the
// project before touching the server: a project that cannot be scanned must
// not leave a freshly created database behind.
type SessionPreparer interface {
	ScanProject(sourcePath string) (scanner.Result, error)
	PrepareSession(ctx context.Context, connConfig *db.ConnectionConfig, scanResult scanner.Result, parameters map[string]string, compat string, verbose bool) (*session.Session, error)
}

// DatabaseManager is what the deployment needs from database lifecycle
// management. Implementations are not safe for concurrent use.
type DatabaseManager interface {
	// Exists checks if a database exists.
	Exists(ctx context.Context, conn db.DBConnection, dbName string) (bool, error)

	// Settings reports the properties an existing database was created with, so
	// --overwrite can recreate it the same way. Returns nil when the database
	// is absent, or when it matches the server defaults and there is nothing to
	// preserve.
	Settings(ctx context.Context, conn db.DBConnection, dbName string) (*manager.DatabaseSettings, error)

	// Create creates a new database. A nil settings uses the server defaults,
	// inheriting template1 as PostgreSQL does by default.
	Create(ctx context.Context, conn db.DBConnection, dbName string, settings *manager.DatabaseSettings) error

	// RestoreGaps names every setting, comment, privilege or owner of an
	// existing database that the connecting role could not restore after a
	// drop. --overwrite refuses before dropping when it is not empty.
	RestoreGaps(ctx context.Context, conn db.DBConnection, dbName string) ([]string, error)

	// Drop drops the specified database.
	Drop(ctx context.Context, conn db.DBConnection, dbName string) error

	// LockOverwrite holds the lock that serialises --overwrite runs on dbName
	// until unlock is called, and returns the connection holding it; held is
	// nil when another run holds the lock.
	LockOverwrite(ctx context.Context, conn db.DBConnection, dbName string) (held db.DBConnection, unlock func(), err error)

	// ServerVersion reports the server's version as server_version_num and as text.
	ServerVersion(ctx context.Context, conn db.DBConnection) (num int, text string, err error)

	// DeployInProgress reports whether a pgmi deploy holds its lock on dbName.
	DeployInProgress(ctx context.Context, conn db.DBConnection, dbName string) (bool, error)
}
