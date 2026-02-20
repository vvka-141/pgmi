package manager

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

const (
	queryDatabaseExists      = "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)"
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
		return false, fmt.Errorf("failed to check database existence: %w", err)
	}
	return exists, nil
}

// Create creates a new database.
func (m *Manager) Create(ctx context.Context, conn pgmi.DBConnection, dbName string) error {
	pooledConn, err := conn.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer pooledConn.Release()

	query := fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{dbName}.Sanitize())
	_, err = pooledConn.Exec(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create database %q: %w", dbName, err)
	}
	return nil
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
