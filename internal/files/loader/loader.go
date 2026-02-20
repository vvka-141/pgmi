package loader

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
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

// execBatch sends a prepared batch and checks each result.
// labels must be parallel to the batch entries (one per queued statement).
func execBatch(ctx context.Context, conn *pgxpool.Conn, batch *pgx.Batch, labels []string, itemErr, closeErr string) error {
	results := conn.SendBatch(ctx, batch)

	for _, label := range labels {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return fmt.Errorf("%s %s: %w", itemErr, label, err)
		}
	}

	if err := results.Close(); err != nil {
		return fmt.Errorf("%s: %w", closeErr, err)
	}

	return nil
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

	if err := l.insertFiles(ctx, conn, sourceFiles); err != nil {
		return fmt.Errorf("failed to insert source files: %w", err)
	}

	// Insert test directories first (pgmi_test_source has FK to pgmi_test_directory)
	if err := l.insertTestDirectories(ctx, conn, testFiles); err != nil {
		return fmt.Errorf("failed to insert test directories: %w", err)
	}

	if err := l.insertTestFiles(ctx, conn, testFiles); err != nil {
		return fmt.Errorf("failed to insert test files: %w", err)
	}

	if err := l.insertMetadata(ctx, conn, sourceFiles); err != nil {
		return fmt.Errorf("failed to insert metadata: %w", err)
	}

	return nil
}

// insertTestFiles inserts test file content into pg_temp.pgmi_test_source.
// Only SQL files are inserted (non-SQL files like README.md are skipped).
func (l *Loader) insertTestFiles(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	if len(files) == 0 {
		return nil
	}

	insertSQL := `INSERT INTO pg_temp._pgmi_test_source (path, directory, filename, content, is_fixture) VALUES ($1, $2, $3, $4, $5)`

	var sqlFiles []pgmi.FileMetadata
	for _, file := range files {
		if pgmi.IsSQLExtension(file.Extension) {
			sqlFiles = append(sqlFiles, file)
		}
	}

	if len(sqlFiles) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	labels := make([]string, len(sqlFiles))
	for i, file := range sqlFiles {
		filename := filepath.Base(file.Path)
		directory := extractTestDirectory(file.Path)
		isFixture := isFixtureFile(filename)
		batch.Queue(insertSQL, file.Path, directory, filename, file.Content, isFixture)
		labels[i] = file.Path
	}

	return execBatch(ctx, conn, batch, labels,
		"failed to insert test file",
		"failed to complete test file batch insert")
}

// insertTestDirectories extracts unique test directories from file paths and inserts them.
// Computes parent relationships and depth for hierarchical traversal.
func (l *Loader) insertTestDirectories(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	if len(files) == 0 {
		return nil
	}

	dirSet := make(map[string]bool)
	for _, file := range files {
		dir := extractTestDirectory(file.Path)
		if dir != "" {
			dirSet[dir] = true
		}
	}

	if len(dirSet) == 0 {
		return nil
	}

	dirs := make([]string, 0, len(dirSet))
	for dir := range dirSet {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	type dirInfo struct {
		path       string
		parentPath *string
		depth      int
	}

	dirInfos := make([]dirInfo, 0, len(dirs))
	for _, dir := range dirs {
		parent := findParentTestDirectory(dir, dirSet)
		depth := countTestDirectoryDepth(dir)
		dirInfos = append(dirInfos, dirInfo{
			path:       dir,
			parentPath: parent,
			depth:      depth,
		})
	}

	// Sort by depth to ensure parents are inserted first (FK constraint)
	sort.Slice(dirInfos, func(i, j int) bool {
		return dirInfos[i].depth < dirInfos[j].depth
	})

	insertSQL := `INSERT INTO pg_temp._pgmi_test_directory (path, parent_path, depth) VALUES ($1, $2, $3)`

	batch := &pgx.Batch{}
	labels := make([]string, len(dirInfos))
	for i, info := range dirInfos {
		batch.Queue(insertSQL, info.path, info.parentPath, info.depth)
		labels[i] = info.path
	}

	return execBatch(ctx, conn, batch, labels,
		"failed to insert test directory",
		"failed to complete test directory batch insert")
}

// extractTestDirectory extracts the full directory path from a test file path.
// Returns the directory path ending with /, e.g., "./__test__/auth/" from "./__test__/auth/test.sql"
// Only processes paths that contain __test__ or __tests__.
func extractTestDirectory(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")

	if !strings.Contains(path, "/__test__/") && !strings.Contains(path, "/__tests__/") {
		return ""
	}

	lastSlash := strings.LastIndex(path, "/")
	if lastSlash == -1 {
		return ""
	}
	return path[:lastSlash+1]
}

// findParentTestDirectory finds the parent directory in the test hierarchy.
// Returns nil if no parent exists in the directory set.
func findParentTestDirectory(dir string, dirSet map[string]bool) *string {
	trimmed := strings.TrimSuffix(dir, "/")
	parts := strings.Split(trimmed, "/")

	if len(parts) < 2 {
		return nil
	}

	parentPath := strings.Join(parts[:len(parts)-1], "/") + "/"
	if dirSet[parentPath] {
		return &parentPath
	}
	return nil
}

// countTestDirectoryDepth counts how deep the directory is within the test hierarchy.
// The root __test__/ directory has depth 0, subdirectories increment from there.
func countTestDirectoryDepth(path string) int {
	path = strings.ReplaceAll(path, "\\", "/")

	testPattern := regexp.MustCompile(`/__tests?__/`)
	loc := testPattern.FindStringIndex(path)
	if loc == nil {
		return 0
	}

	afterTest := path[loc[1]:]
	afterTest = strings.TrimSuffix(afterTest, "/")
	if afterTest == "" {
		return 0
	}

	return strings.Count(afterTest, "/") + 1
}

// isFixtureFile checks if a filename is a fixture setup file.
// Case-insensitive match for _setup.sql or _setup.psql
func isFixtureFile(filename string) bool {
	lower := strings.ToLower(filename)
	return lower == "_setup.sql" || lower == "_setup.psql"
}

// insertFiles inserts file metadata into the pg_temp.pgmi_source table using the pgmi_register_file function.
func (l *Loader) insertFiles(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	if len(files) == 0 {
		return nil
	}

	insertSQL := `SELECT pg_temp.pgmi_register_file($1, $2, $3, $4)`

	batch := &pgx.Batch{}
	labels := make([]string, len(files))
	for i, file := range files {
		batch.Queue(insertSQL, file.Path, file.Content, file.ChecksumRaw, file.Checksum)
		labels[i] = file.Path
	}

	return execBatch(ctx, conn, batch, labels,
		"failed to insert file",
		"failed to complete file batch insert")
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
		return nil
	}

	insertSQL := `INSERT INTO pg_temp._pgmi_parameter (key, value) VALUES (LOWER($1), $2)`

	batch := &pgx.Batch{}
	labels := make([]string, 0, len(params))
	for key, value := range params {
		batch.Queue(insertSQL, key, value)
		labels = append(labels, key)
	}

	return execBatch(ctx, conn, batch, labels,
		"failed to insert parameter",
		"failed to complete parameter batch insert")
}

// setSessionVariables sets PostgreSQL session variables for all CLI parameters.
// Each parameter becomes accessible as current_setting('pgmi.key').
//
// Precondition: All keys must be pre-validated via validateParameterKey.
// LoadParametersIntoSession validates before calling this function.
func (l *Loader) setSessionVariables(ctx context.Context, conn *pgxpool.Conn, params map[string]string) error {
	if len(params) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	labels := make([]string, 0, len(params))
	for key, value := range params {
		sessionVar := fmt.Sprintf("pgmi.%s", strings.ToLower(key))
		batch.Queue("SELECT set_config($1, $2, false)", sessionVar, value)
		labels = append(labels, key)
	}

	return execBatch(ctx, conn, batch, labels,
		"failed to set session variable for parameter",
		"failed to complete session variable batch set")
}

// insertMetadata inserts script metadata into the pg_temp.pgmi_source_metadata table.
// Only processes files that have metadata (FileMetadata.Metadata != nil).
func (l *Loader) insertMetadata(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	insertSQL := `
		INSERT INTO pg_temp._pgmi_source_metadata
		(path, id, idempotent, sort_keys, description)
		VALUES ($1, $2, $3, $4, $5)
	`

	batch := &pgx.Batch{}
	var labels []string
	for _, file := range files {
		if file.Metadata == nil {
			continue
		}
		batch.Queue(insertSQL,
			file.Path,
			file.Metadata.ID,
			file.Metadata.Idempotent,
			file.Metadata.SortKeys,
			file.Metadata.Description,
		)
		labels = append(labels, file.Path)
	}

	if len(labels) == 0 {
		return nil
	}

	return execBatch(ctx, conn, batch, labels,
		"failed to insert metadata for file",
		"failed to complete metadata batch insert")
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
