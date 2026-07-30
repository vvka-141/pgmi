package loader

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

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
// Non-test files go into pg_temp._pgmi_source, test files go into pg_temp._pgmi_test_source.
// Also loads script metadata (from <pgmi-meta> XML blocks) into pg_temp._pgmi_source_metadata.
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

	if len(files) == 0 {
		return nil
	}
	return l.analyzeSessionTables(ctx, conn)
}

// analyzeSessionTables gives the planner statistics for the tables just filled.
//
// Autovacuum cannot see a temp table, so without this every plan is built on
// default estimates. pgmi_test_plan is a recursive CTE joined against them, and
// on a 20-directory project it took 546ms per call that way and 1.8ms after —
// paid on every deploy, since each deploy is a fresh session.
//
// ANALYZE is legal inside a transaction block; VACUUM is not, and is not needed
// here — these tables have no dead rows.
func (l *Loader) analyzeSessionTables(ctx context.Context, conn *pgxpool.Conn) error {
	const stmt = `ANALYZE pg_temp._pgmi_source, pg_temp._pgmi_source_metadata,
	              pg_temp._pgmi_test_source, pg_temp._pgmi_test_directory`
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("failed to analyze session tables: %w", err)
	}
	return nil
}

// insertTestFiles inserts test file content into pg_temp._pgmi_test_source.
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
	slices.SortFunc(dirs, cmp.Compare)

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

	slices.SortFunc(dirInfos, func(a, b dirInfo) int {
		return cmp.Compare(a.depth, b.depth)
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

// findParentTestDirectory finds the nearest ancestor of dir that is itself in
// the directory set, or nil when dir is a tree root.
//
// Only directories that directly hold a file enter the set, so an intermediate
// level can be missing: __test__/sub/deep/test_b.sql with nothing in
// __test__/sub/. Matching only the immediate parent left such a directory
// parentless, pgmi_test_plan treated it as a second tree root, and the root
// teardown was emitted before the nested test — which then ran against a
// rolled-back parent fixture.
func findParentTestDirectory(dir string, dirSet map[string]bool) *string {
	parts := strings.Split(strings.TrimSuffix(dir, "/"), "/")

	for i := len(parts) - 1; i >= 1; i-- {
		candidate := strings.Join(parts[:i], "/") + "/"
		if dirSet[candidate] {
			return &candidate
		}
	}
	return nil
}

// countTestDirectoryDepth counts how deep the directory is within the test hierarchy.
// The root __test__/ directory has depth 0, subdirectories increment from there.
var testDirPattern = regexp.MustCompile(`/__tests?__/`)

func countTestDirectoryDepth(path string) int {
	path = strings.ReplaceAll(path, "\\", "/")

	loc := testDirPattern.FindStringIndex(path)
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

// insertFiles inserts file metadata into the pg_temp._pgmi_source table using the pgmi_register_file function.
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

// LoadParametersIntoSession loads parameters into the pg_temp._pgmi_parameter table
// and automatically sets them as PostgreSQL session variables with 'pgmi.' prefix.
//
// This eliminates the need for users to call pgmi_init_params() in deploy.sql.
// Parameters are immediately accessible via current_setting('pgmi.key').
func (l *Loader) LoadParametersIntoSession(ctx context.Context, conn *pgxpool.Conn, params map[string]string) error {
	for key, value := range params {
		if err := validateParameterKey(key); err != nil {
			return fmt.Errorf("%w: %w", pgmi.ErrInvalidConfig, err)
		}
		if err := validateParameterValue(key, value); err != nil {
			return fmt.Errorf("%w: %w", pgmi.ErrInvalidConfig, err)
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

// insertParams inserts parameters into the pg_temp._pgmi_parameter table using batch insert.
//
// Keys are lower-cased so the view names the same thing the session variable
// does: setSessionVariables builds pgmi.<lower(key)>, and PostgreSQL's GUC
// namespace is case-insensitive anyway. Preserving the original case here would
// make pgmi_parameter_view.key disagree with the variable actually set.
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

// insertMetadata inserts script metadata into the pg_temp._pgmi_source_metadata table.
// Only processes files that have metadata (FileMetadata.Metadata != nil).
func (l *Loader) insertMetadata(ctx context.Context, conn *pgxpool.Conn, files []pgmi.FileMetadata) error {
	insertSQL := `INSERT INTO pg_temp._pgmi_source_metadata (path, id, idempotent, sort_keys, description) VALUES ($1, $2, $3, $4, $5)`

	batch := &pgx.Batch{}
	var labels []string
	for _, file := range files {
		if file.Metadata == nil {
			continue
		}
		// sortKeys is optional in <pgmi-meta>, and absent it parses to a nil
		// slice, which pgx sends as SQL NULL — violating the column's NOT NULL
		// and failing the deploy with a raw 23502. The column defaults to '{}'
		// for exactly this case, and pgmi_plan_view's fallback is written for
		// it too: NULLIF(m.sort_keys, '{}') is what makes such a file order by
		// path. Send the empty array the rest of the system expects.
		sortKeys := file.Metadata.SortKeys
		if sortKeys == nil {
			sortKeys = []string{}
		}

		batch.Queue(insertSQL,
			file.Path,
			file.Metadata.ID,
			file.Metadata.Idempotent,
			sortKeys,
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

// A parameter becomes the GUC pgmi.<key>, so the key has to be a PostgreSQL
// simple identifier: letter or underscore first, then letters, digits or
// underscores. The old pattern allowed a leading digit, which PostgreSQL
// refuses — `--param 1abc=v` passed this check and then died inside session
// preparation with a raw 42602 ("Custom parameter names must be two or more
// simple identifiers separated by dots") and exit 1, instead of being named
// here and exiting 10 like every other malformed key.
var keyPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

func validateParameterKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("invalid parameter key '%s': must start with a letter or underscore and contain only letters, digits and underscores, 1-63 characters (PostgreSQL identifier limit)", key)
	}
	return nil
}

// validateParameterValue rejects NUL bytes and invalid UTF-8. Both would
// cause pgx to fail cryptically much later; fail fast at the boundary
// instead. Values ARE allowed to contain any other character including
// control chars — PostgreSQL session GUCs accept them, and we do not
// interpret the content ourselves.
func validateParameterValue(key, value string) error {
	if idx := strings.IndexByte(value, 0); idx >= 0 {
		return fmt.Errorf("invalid parameter value for '%s': contains NUL byte at offset %d", key, idx)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("invalid parameter value for '%s': not valid UTF-8", key)
	}
	return nil
}

// Verify Loader implements the interface at compile time
var _ pgmi.FileLoader = (*Loader)(nil)
