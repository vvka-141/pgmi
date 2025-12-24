package manager

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Manager implements database lifecycle operations using the DBConnection abstraction.
// This implementation removes the unnecessary delegation layer while maintaining
// clean separation from pgx-specific types.
//
// Thread-Safety: NOT safe for concurrent use. Create separate instances for
// concurrent operations.
type Manager struct{}

// New creates a new DatabaseManager instance.
func New() pgmi.DatabaseManager {
	return &Manager{}
}

// Exists checks if a database exists.
func (m *Manager) Exists(ctx context.Context, conn pgmi.DBConnection, dbName string) (bool, error) {
	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)"
	err := conn.QueryRow(ctx, query, dbName).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check database existence: %w", err)
	}
	return exists, nil
}

// Create creates a new database.
// Uses pgx.Identifier.Sanitize() to safely quote the database name and prevent SQL injection.
func (m *Manager) Create(ctx context.Context, conn pgmi.DBConnection, dbName string) error {
	pooledConn, err := conn.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer pooledConn.Release()

	// Use pgx.Identifier.Sanitize() to safely quote the database name
	// and prevent SQL injection. This properly escapes special characters
	// and handles edge cases like database names with spaces or quotes.
	query := fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{dbName}.Sanitize())
	_, err = pooledConn.Exec(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create database %q: %w", dbName, err)
	}
	return nil
}

// Drop drops the specified database.
// Uses pgx.Identifier.Sanitize() to safely quote the database name and prevent SQL injection.
func (m *Manager) Drop(ctx context.Context, conn pgmi.DBConnection, dbName string) error {
	// DROP DATABASE cannot be executed in a transaction, so we need a direct connection
	pooledConn, err := conn.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer pooledConn.Release()

	// Use pgx.Identifier.Sanitize() to safely quote the database name
	// and prevent SQL injection. This properly escapes special characters
	// and handles edge cases like database names with spaces or quotes.
	query := fmt.Sprintf("DROP DATABASE %s", pgx.Identifier{dbName}.Sanitize())
	_, err = pooledConn.Exec(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to drop database %q: %w", dbName, err)
	}
	return nil
}

// TerminateConnections terminates all connections to the specified database.
// This is typically used before dropping a database to ensure no active connections remain.
func (m *Manager) TerminateConnections(ctx context.Context, conn pgmi.DBConnection, dbName string) error {
	query := `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE datname = $1 AND pid <> pg_backend_pid()
	`
	_, err := conn.Exec(ctx, query, dbName)
	if err != nil {
		return fmt.Errorf("failed to terminate connections to database %q: %w", dbName, err)
	}
	return nil
}

// Verify Manager implements the DatabaseManager interface at compile time
var _ pgmi.DatabaseManager = (*Manager)(nil)
