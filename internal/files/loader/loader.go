package loader

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/testdiscovery"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Loader handles loading file metadata into the PostgreSQL session.
type Loader struct{}

// NewLoader creates a new file loader.
func NewLoader() *Loader {
	return &Loader{}
}

// LoadFilesIntoSession loads file metadata into session-scoped tables.
// Non-test files go into pg_temp.pgmi_source, test files go into pg_temp.pgmi_test_source.
// Also loads script metadata (from <pgmi:meta> XML blocks) into pg_temp.pgmi_source_metadata.
func (l *Loader) LoadFilesIntoSession(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	// Separate test files from non-test files
	var sourceFiles, testFiles []pgmi.FileMetadata
	for _, f := range files {
		if pgmi.IsTestPath(f.Path) {
			testFiles = append(testFiles, f)
		} else {
			sourceFiles = append(sourceFiles, f)
		}
	}

	// Insert non-test files into pgmi_source
	if err := l.insertFiles(ctx, conn, sourceFiles); err != nil {
		return fmt.Errorf("failed to insert source files: %w", err)
	}

	// Insert test files into pgmi_test_source
	if err := l.insertTestFiles(ctx, conn, testFiles); err != nil {
		return fmt.Errorf("failed to insert test files: %w", err)
	}

	// Insert script metadata (only for non-test files with metadata)
	if err := l.insertMetadata(ctx, conn, sourceFiles); err != nil {
		return fmt.Errorf("failed to insert metadata: %w", err)
	}

	return nil
}

// insertTestFiles inserts test file content into pg_temp.pgmi_test_source.
// Uses batch insert for performance.
func (l *Loader) insertTestFiles(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	if len(files) == 0 {
		return nil
	}

	insertSQL := `INSERT INTO pg_temp.pgmi_test_source (path, content) VALUES ($1, $2)`

	batch := &pgx.Batch{}
	for _, file := range files {
		batch.Queue(insertSQL, file.Path, file.Content)
	}

	results := conn.SendBatch(ctx, batch)
	defer results.Close()

	for i := range files {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("failed to insert test file %s: %w", files[i].Path, err)
		}
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
	for key := range params {
		if err := validateParameterKey(key); err != nil {
			return err
		}
	}

	if err := l.insertParams(ctx, conn, params); err != nil {
		return fmt.Errorf("failed to insert parameters: %w", err)
	}

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
		return nil
	}

	for key := range params {
		if err := validateParameterKey(key); err != nil {
			return err
		}
	}

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

// LoadTestScriptsIntoSession discovers tests from the loaded files and populates pgmi_test_plan.
// This enables the pgmi_test() macro to execute tests with proper savepoint structure.
// Content is embedded directly in the plan rows for self-contained execution.
func (l *Loader) LoadTestScriptsIntoSession(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	// Build content map from test files
	contentMap := make(map[string]string)
	for _, f := range files {
		if pgmi.IsTestPath(f.Path) {
			contentMap[f.Path] = f.Content
		}
	}

	if len(contentMap) == 0 {
		return nil // No test files to process
	}

	// Create content resolver
	resolver := func(path string) (string, error) {
		if content, ok := contentMap[path]; ok {
			return content, nil
		}
		return "", fmt.Errorf("test file not found: %s", path)
	}

	// Convert FileMetadata to Source for discovery
	sources := testdiscovery.ConvertFromFileMetadata(files)

	// Discover test tree
	discoverer := testdiscovery.NewDiscoverer(nil)
	tree, err := discoverer.Discover(sources)
	if err != nil {
		return fmt.Errorf("test discovery failed: %w", err)
	}

	// Build execution plan with embedded content
	planBuilder := testdiscovery.NewPlanBuilder(resolver)
	rows, err := planBuilder.Build(tree)
	if err != nil {
		return fmt.Errorf("test plan build failed: %w", err)
	}

	if len(rows) == 0 {
		return nil // No tests to load
	}

	// Insert into pgmi_test_plan
	insertSQL := `
		INSERT INTO pg_temp.pgmi_test_plan
		(ordinal, step_type, script_path, directory, depth, pre_exec, script_sql, post_exec)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(insertSQL,
			row.Ordinal,
			row.StepType,
			row.ScriptPath,
			row.Directory,
			row.Depth,
			row.PreExec,
			row.ScriptSQL,
			row.PostExec,
		)
	}

	results := conn.SendBatch(ctx, batch)
	defer results.Close()

	for i := range rows {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("failed to insert test plan row %d: %w", i, err)
		}
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
