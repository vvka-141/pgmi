package loader

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Loader handles loading file metadata into the PostgreSQL session.
type Loader struct{}

// NewLoader creates a new file loader.
func NewLoader() *Loader {
	return &Loader{}
}

// LoadFilesIntoSession creates the pg_temp.pgmi_source table and loads file metadata.
// Also loads script metadata (from <pgmi:meta> XML blocks) into pg_temp.pgmi_source_metadata.
func (l *Loader) LoadFilesIntoSession(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	// Insert file metadata using the pg_temp.pgmi_register_file function
	if err := l.insertFiles(ctx, conn, files); err != nil {
		return fmt.Errorf("failed to insert files: %w", err)
	}

	// Insert script metadata (only for files with metadata)
	if err := l.insertMetadata(ctx, conn, files); err != nil {
		return fmt.Errorf("failed to insert metadata: %w", err)
	}

	return nil
}

// insertFiles inserts file metadata into the pg_temp.pgmi_source table using the pgmi_register_file function.
// This provides 10-100x performance improvement over individual INSERTs for large file sets.
func (l *Loader) insertFiles(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	if len(files) == 0 {
		return nil // Nothing to insert
	}

	insertSQL := `SELECT pg_temp.pgmi_register_file($1, $2, $3, $4)`

	// Use batch insert for performance
	batch := &pgx.Batch{}
	for _, file := range files {
		batch.Queue(insertSQL,
			file.Path,
			file.Content,
			file.ChecksumRaw, // raw checksum
			file.Checksum,    // normalized checksum (pgmi_checksum)
		)
	}

	// Send batch and process results
	results := conn.SendBatch(ctx, batch)
	defer results.Close()

	// Check each result for errors
	for i := range files {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("failed to insert file %s: %w", files[i].Path, err)
		}
	}

	return nil
}

// LoadParametersIntoSession loads parameters into the pg_temp.pgmi_parameter table
// and automatically sets them as PostgreSQL session variables with 'pgmi.' prefix.
//
// This eliminates the need for users to call pgmi_init_params() in deploy.sql.
// Parameters are immediately accessible via current_setting('pgmi.key').
func (l *Loader) LoadParametersIntoSession(ctx context.Context, conn *pgxpool.Conn, params map[string]string) error {
	// Insert parameters into table
	if err := l.insertParams(ctx, conn, params); err != nil {
		return fmt.Errorf("failed to insert parameters: %w", err)
	}

	// Auto-initialize session variables from CLI parameters
	// This makes parameters immediately accessible without requiring pgmi_init_params() call
	if err := l.setSessionVariables(ctx, conn, params); err != nil {
		return fmt.Errorf("failed to set session variables: %w", err)
	}

	return nil
}

// insertParams inserts parameters into the pg_temp.pgmi_parameter table using batch insert.
// Keys are normalized to lowercase for case-insensitive lookups.
func (l *Loader) insertParams(ctx context.Context, conn *pgxpool.Conn, params map[string]string) error {
	if len(params) == 0 {
		return nil // Nothing to insert
	}

	insertSQL := `INSERT INTO pg_temp.pgmi_parameter (key, value) VALUES (LOWER($1), $2)`

	// Use batch insert for performance (even though params are typically small)
	batch := &pgx.Batch{}
	paramKeys := make([]string, 0, len(params))

	for key, value := range params {
		batch.Queue(insertSQL, key, value)
		paramKeys = append(paramKeys, key)
	}

	// Send batch and process results
	results := conn.SendBatch(ctx, batch)
	defer results.Close()

	// Check each result for errors
	for i := range paramKeys {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("failed to insert parameter %s: %w", paramKeys[i], err)
		}
	}

	return nil
}

// setSessionVariables sets PostgreSQL session variables for all CLI parameters.
// Each parameter becomes accessible as current_setting('pgmi.key').
//
// Security considerations:
//   - Parameter keys are validated (alphanumeric + underscore only)
//   - Maximum key length: 63 characters (PostgreSQL identifier limit)
//   - Session variables are session-scoped (not persisted across connections)
//   - Uses parameterized queries (no SQL injection risk)
func (l *Loader) setSessionVariables(ctx context.Context, conn *pgxpool.Conn, params map[string]string) error {
	if len(params) == 0 {
		return nil // No parameters to set
	}

	for key := range params {
		if err := validateParameterKey(key); err != nil {
			return err
		}
	}

	// Use batch for performance (even though params are typically small)
	batch := &pgx.Batch{}
	paramKeys := make([]string, 0, len(params))

	for key, value := range params {
		// Normalize key to lowercase (consistent with insertParams)
		sessionVar := fmt.Sprintf("pgmi.%s", strings.ToLower(key))

		// Queue set_config() call
		// set_config(setting_name, new_value, is_local)
		// is_local=false makes the setting session-scoped (resets on connection close)
		batch.Queue("SELECT set_config($1, $2, false)", sessionVar, value)
		paramKeys = append(paramKeys, key)
	}

	// Send batch and process results
	results := conn.SendBatch(ctx, batch)
	defer results.Close()

	// Check each result for errors
	for i := range paramKeys {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("failed to set session variable for parameter %s: %w", paramKeys[i], err)
		}
	}

	return nil
}

// insertMetadata inserts script metadata into the pg_temp.pgmi_source_metadata table.
// Only processes files that have metadata (FileMetadata.Metadata != nil).
// Uses batch insert for performance.
func (l *Loader) insertMetadata(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	// Count files with metadata
	metadataCount := 0
	for _, file := range files {
		if file.Metadata != nil {
			metadataCount++
		}
	}

	if metadataCount == 0 {
		return nil // No metadata to insert
	}

	insertSQL := `
		INSERT INTO pg_temp.pgmi_source_metadata
		(path, id, idempotent, sort_keys, description)
		VALUES ($1, $2, $3, $4, $5)
	`

	// Build batch for files with metadata
	batch := &pgx.Batch{}
	for _, file := range files {
		if file.Metadata == nil {
			continue // Skip files without metadata
		}

		batch.Queue(insertSQL,
			file.Path,
			file.Metadata.ID,
			file.Metadata.Idempotent,
			file.Metadata.SortKeys,
			file.Metadata.Description,
		)
	}

	// Send batch and process results
	results := conn.SendBatch(ctx, batch)
	defer results.Close()

	// Check each result for errors
	insertedCount := 0
	for _, file := range files {
		if file.Metadata == nil {
			continue
		}
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("failed to insert metadata for file %s: %w", file.Path, err)
		}
		insertedCount++
	}

	return nil
}

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_]{1,63}$`)

func validateParameterKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("invalid parameter key '%s': must be alphanumeric with underscores, 1-63 characters (PostgreSQL identifier limit)", key)
	}
	return nil
}

// Verify Loader implements the interface at compile time
var _ pgmi.FileLoader = (*Loader)(nil)
