package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

const (
	queryDatabaseExists       = "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)"
	queryTerminateConnections = `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE datname = $1 AND pid <> pg_backend_pid()
	`
)

// Manager implements database lifecycle operations using the DBConnection abstraction.
// Stateless and safe for concurrent use; thread safety depends on the injected DBConnection.
type Manager struct{}

// New creates a new DatabaseManager instance.
func New() pgmi.DatabaseManager {
	return &Manager{}
}

// Exists checks if a database exists.
func (m *Manager) Exists(ctx context.Context, conn pgmi.DBConnection, dbName string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx, queryDatabaseExists, dbName).Scan(&exists)
	if err != nil {
		// Named like its siblings: Create, Drop and TerminateConnections all
		// report which database they were working on, and the caller adds no
		// wrapper of its own.
		return false, fmt.Errorf("failed to check existence of database %q: %w", dbName, err)
	}
	return exists, nil
}

// Settings reports the encoding and locale an existing database carries, but
// only when they differ from what a bare CREATE DATABASE would produce.
//
// Comparing against template1 is the point: that is the template PostgreSQL
// copies by default, so a database matching it needs nothing preserved and
// keeps inheriting whatever a site has installed there. Only a database that
// differs has to be recreated explicitly, and that forces template0.
func (m *Manager) Settings(ctx context.Context, conn pgmi.DBConnection, dbName string) (*pgmi.DatabaseSettings, error) {
	const q = `
		SELECT pg_encoding_to_char(d.encoding), d.datcollate, d.datctype,
		       pg_get_userbyid(d.datdba), d.datconnlimit,
		       coalesce((SELECT s.setconfig FROM pg_db_role_setting s
		                 WHERE s.setdatabase = d.oid AND s.setrole = 0), '{}'),
		       shobj_description(d.oid, 'pg_database'),
		       pg_encoding_to_char(t.encoding), t.datcollate, t.datctype
		FROM pg_database d, pg_database t
		WHERE d.datname = $1 AND t.datname = 'template1'`

	var s, def pgmi.DatabaseSettings
	err := conn.QueryRow(ctx, q, dbName).Scan(
		&s.Encoding, &s.Collate, &s.CType,
		&s.Owner, &s.ConnectionLimit, &s.Options, &s.Comment,
		&def.Encoding, &def.Collate, &def.CType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // absent, so there is nothing to preserve
		}
		return nil, fmt.Errorf("failed to read settings of database %q: %w", dbName, err)
	}
	s.PreserveLocale = s.Encoding != def.Encoding || s.Collate != def.Collate || s.CType != def.CType
	return &s, nil
}

// Create creates a new database, preserving settings when given.
func (m *Manager) Create(ctx context.Context, conn pgmi.DBConnection, dbName string, settings *pgmi.DatabaseSettings) error {
	pooledConn, err := conn.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer pooledConn.Release()

	ident := pgx.Identifier{dbName}.Sanitize()
	query := "CREATE DATABASE " + ident
	if settings != nil {
		if settings.PreserveLocale {
			// template0 is mandatory, not a preference: PostgreSQL refuses a
			// non-default encoding or locale copied from template1, because
			// whatever a site installed there may not survive the conversion.
			query += fmt.Sprintf(" TEMPLATE template0 ENCODING %s LC_COLLATE %s LC_CTYPE %s",
				quoteLiteral(settings.Encoding), quoteLiteral(settings.Collate), quoteLiteral(settings.CType))
		}
		if settings.Owner != "" {
			query += " OWNER " + pgx.Identifier{settings.Owner}.Sanitize()
		}
		if settings.ConnectionLimit >= 0 {
			query += fmt.Sprintf(" CONNECTION LIMIT %d", settings.ConnectionLimit)
		}
	}
	if _, err = pooledConn.Exec(ctx, query); err != nil {
		return fmt.Errorf("failed to create database %q: %w", dbName, err)
	}

	if settings == nil {
		return nil
	}

	// The rest cannot ride along on CREATE DATABASE and have to be reapplied.
	// Each is something the operator set deliberately — a per-database
	// statement_timeout is a safeguard, and losing it silently is worse than
	// never having had it.
	for _, opt := range settings.Options {
		name, value, found := strings.Cut(opt, "=")
		if !found {
			continue
		}
		alter := fmt.Sprintf("ALTER DATABASE %s SET %s = %s",
			ident, pgx.Identifier{name}.Sanitize(), quoteLiteral(value))
		if _, err := pooledConn.Exec(ctx, alter); err != nil {
			return fmt.Errorf("failed to restore setting %q on database %q: %w", name, dbName, err)
		}
	}
	if settings.Comment != nil {
		comment := fmt.Sprintf("COMMENT ON DATABASE %s IS %s", ident, quoteLiteral(*settings.Comment))
		if _, err := pooledConn.Exec(ctx, comment); err != nil {
			return fmt.Errorf("failed to restore the comment on database %q: %w", dbName, err)
		}
	}
	return nil
}

// quoteLiteral renders a SQL string literal. CREATE DATABASE takes no
// parameters, and although these values come straight back from pg_database,
// building SQL by concatenation without quoting is a habit worth not forming.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// Drop drops the specified database.
func (m *Manager) Drop(ctx context.Context, conn pgmi.DBConnection, dbName string) error {
	pooledConn, err := conn.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer pooledConn.Release()

	query := fmt.Sprintf("DROP DATABASE %s", pgx.Identifier{dbName}.Sanitize())
	_, err = pooledConn.Exec(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to drop database %q: %w", dbName, err)
	}
	return nil
}

// TerminateConnections terminates all connections to the specified database.
func (m *Manager) TerminateConnections(ctx context.Context, conn pgmi.DBConnection, dbName string) error {
	_, err := conn.Exec(ctx, queryTerminateConnections, dbName)
	if err != nil {
		return fmt.Errorf("failed to terminate connections to database %q: %w", dbName, err)
	}
	return nil
}

// Verify Manager implements the DatabaseManager interface at compile time
var _ pgmi.DatabaseManager = (*Manager)(nil)
