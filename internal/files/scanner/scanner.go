package scanner

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/metadata"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Scanner discovers and processes files from a directory tree.
// All file types are loaded into pg_temp._pgmi_source; use is_sql_file column
// to filter SQL files in deploy.sql if needed.
// Scanner is safe for concurrent use by multiple goroutines as long as
// the provided calculator and fsProvider are also thread-safe.
type Scanner struct {
	calculator checksum.Calculator
	fsProvider filesystem.FileSystemProvider
}

// NewScanner creates a new file scanner with the given checksum calculator.
// Uses OS filesystem by default.
// Panics if calculator is nil.
func NewScanner(calculator checksum.Calculator) *Scanner {
	if calculator == nil {
		panic("calculator cannot be nil")
	}
	return &Scanner{
		calculator: calculator,
		fsProvider: filesystem.NewOSFileSystem(),
	}
}

// NewScannerWithFS creates a new file scanner with a custom filesystem provider.
// This is primarily useful for testing with in-memory filesystems.
// Panics if calculator or fsProvider is nil.
func NewScannerWithFS(calculator checksum.Calculator, fsProvider filesystem.FileSystemProvider) *Scanner {
	if calculator == nil {
		panic("calculator cannot be nil")
	}
	if fsProvider == nil {
		panic("fsProvider cannot be nil")
	}
	return &Scanner{
		calculator: calculator,
		fsProvider: fsProvider,
	}
}

// ScanDirectory recursively scans a directory and returns file metadata.
// It excludes deploy.sql from the results as it's the orchestrator script.
//
// Parameters:
//   - sourcePath: Root directory to scan
//
// Returns:
//   - pgmi.FileScanResult: Scan results including files
//   - error: Any error encountered during scanning
func (s *Scanner) ScanDirectory(sourcePath string) (pgmi.FileScanResult, error) {
	// Open the directory using the filesystem provider
	dir, err := s.fsProvider.Open(sourcePath)
	if err != nil {
		// A project path that does not exist, or is a file, is invalid
		// configuration — exit 10. The deploy path already reported it that way
		// through ValidateDeploySQL, but `pgmi metadata plan|validate|scaffold`
		// call this directly, so the same mistake exited 1 there.
		return pgmi.FileScanResult{}, fmt.Errorf("failed to open directory: %w: %w", err, pgmi.ErrInvalidConfig)
	}

	ignore, err := s.loadPgmiignore(sourcePath)
	if err != nil {
		return pgmi.FileScanResult{}, err
	}

	var (
		files   []pgmi.FileMetadata
		ignored []pgmi.IgnoredPath
	)

	// Walk the directory tree
	err = dir.Walk(func(file filesystem.File, err error) error {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("cannot read %w; exclude it in .pgmiignore or make it readable: %w", err, pgmi.ErrInvalidConfig)
		}
		if err != nil {
			return fmt.Errorf("error walking path: %w", err)
		}

		relPath := filepath.ToSlash(file.RelativePath())
		if relPath == "" || relPath == "." {
			return nil
		}
		isDir := file.Info().IsDir()

		if rule := excludedBy(ignore, relPath, isDir); rule != "" {
			display := "./" + relPath
			if isDir {
				display += "/"
			}
			ignored = append(ignored, pgmi.IgnoredPath{Path: display, Rule: rule})
			// Not descending at all: an unreadable file under .git, node_modules
			// or an ignored folder must not fail a deploy that never loads it.
			if isDir {
				return fs.SkipDir
			}
			return nil
		}
		if isDir {
			return nil
		}

		// Exclude only the root deploy.sql (not nested ones like examples/deploy.sql)
		if strings.ToLower(relPath) == "deploy.sql" {
			return nil
		}

		fileMetadata, err := s.processFile(file)
		if err != nil {
			return err
		}

		files = append(files, fileMetadata)
		return nil
	})

	if err != nil {
		return pgmi.FileScanResult{}, err
	}

	return pgmi.FileScanResult{
		Files:   files,
		Ignored: ignored,
	}, nil
}

// loadPgmiignore reads the project's .pgmiignore, if any. Its patterns follow
// .gitignore syntax and add to the built-in exclusions. A pattern set that
// would hide deploy.sql is refused: without it there is no deployment.
func (s *Scanner) loadPgmiignore(sourcePath string) (*ignoreMatcher, error) {
	content, err := s.fsProvider.ReadFile(filepath.Join(sourcePath, pgmi.PgmiignoreFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", pgmi.PgmiignoreFile, err)
	}

	matcher, err := parseIgnore(string(content))
	if err != nil {
		return nil, fmt.Errorf("%w: %s %w", pgmi.ErrInvalidConfig, pgmi.PgmiignoreFile, err)
	}
	if matcher.Match("deploy.sql", false) {
		return nil, fmt.Errorf("%w: %s excludes deploy.sql; pgmi cannot deploy without it. Remove the pattern, or add !deploy.sql after it",
			pgmi.ErrInvalidConfig, pgmi.PgmiignoreFile)
	}
	return matcher, nil
}

// excludedBy names the rule that keeps relPath out of the session, or "" when
// the path is loaded. The built-in list wins: it is reported even where a
// .pgmiignore pattern would also match.
func excludedBy(ignore *ignoreMatcher, relPath string, isDir bool) string {
	if isExcludedPath(relPath) {
		return pgmi.IgnoreRuleBuiltIn
	}
	if ignore != nil && ignore.Match(relPath, isDir) {
		return pgmi.IgnoreRulePgmiignore
	}
	return ""
}

// excludedDirs are directories whose contents are tooling artifacts rather than
// project files. They are matched by exact name, unlike dot-directories which
// are matched by prefix.
var excludedDirs = map[string]bool{
	"node_modules": true,
	"__pycache__":  true,
}

// isExcludedPath reports whether a file lies under a directory pgmi does not
// load: any dot-directory (.git, .venv, .idea, .claude) or a known tooling
// cache. Discovery decides what enters the session, never what SQL runs, so
// this stays on the infrastructure side of the execution-fabric line.
//
// pgmi's own dunder directories (__test__, __tests__) are deliberately not
// excluded — only the exact names above are.
func isExcludedPath(relPath string) bool {
	for segment := range strings.SplitSeq(filepath.ToSlash(relPath), "/") {
		if segment == "" || segment == "." {
			continue
		}
		if strings.HasPrefix(segment, ".") || excludedDirs[segment] {
			return true
		}
	}
	return false
}

// processFile reads a file and generates its metadata.
// For SQL files, it also extracts and validates PGMI metadata from <pgmi-meta> XML blocks.
// Every error returned here names the offending file in ./ form, so callers
// must not wrap with a path of their own — three layers each restating the
// same file, one of them in OS separators, is what this replaced.
func (s *Scanner) processFile(file filesystem.File) (pgmi.FileMetadata, error) {
	info := file.Info()

	// Convert path to Unix-style (forward slashes) and ensure ./ prefix
	unixPath := filepath.ToSlash(file.RelativePath())
	if !strings.HasPrefix(unixPath, "./") {
		unixPath = "./" + unixPath
	}

	content, err := file.ReadContent()
	if err != nil {
		return pgmi.FileMetadata{}, fmt.Errorf("failed to read %s: %w", unixPath, err)
	}

	// A BOM is valid UTF-8, so it survives the checks below and reaches
	// PostgreSQL glued to the first keyword: EXECUTE on such a file reports a
	// syntax error naming a keyword that looks correct, because the BOM before
	// it is invisible. Windows editors write one by default. Stripping it here
	// — before the checksums and the metadata parse — also keeps a script's
	// identity from changing just because someone opened it in another editor.
	content = bytes.TrimPrefix(content, utf8BOM)

	// Extract directory from normalized path
	// Don't use path.Dir as it removes ./ prefix; instead split on last /
	lastSlash := strings.LastIndex(unixPath, "/")
	var directory string
	if lastSlash == -1 {
		directory = "./"
	} else {
		directory = unixPath[:lastSlash+1]
	}

	// Extract filename and extension
	filename := info.Name()
	extension := filepath.Ext(filename)

	// Calculate checksums
	checksumNormalized := s.calculator.CalculateNormalized(content)
	checksumRaw := s.calculator.CalculateRaw(content)

	if err := pgmi.ValidateDunderDirectories(unixPath); err != nil {
		return pgmi.FileMetadata{}, err
	}

	// pgmi loads project files into PostgreSQL TEXT columns, so a NUL byte or
	// invalid UTF-8 would otherwise surface as a cryptic database error at load
	// time. Fail early with the offending path — pgmi projects are text/source
	// trees.
	if bytes.IndexByte(content, 0) >= 0 {
		return pgmi.FileMetadata{}, fmt.Errorf("file %s contains a NUL byte; pgmi loads project files as text, so exclude it in .pgmiignore or move it out of the project path: %w", unixPath, pgmi.ErrInvalidConfig)
	}
	if !utf8.Valid(content) {
		return pgmi.FileMetadata{}, fmt.Errorf("file %s is not valid UTF-8; pgmi loads project files as text, so exclude it in .pgmiignore or move it out of the project path: %w", unixPath, pgmi.ErrInvalidConfig)
	}

	var scriptMetadata *pgmi.ScriptMetadata
	isTestFile := pgmi.IsTestPath(unixPath)
	isSQLFile := pgmi.IsSQLExtension(extension)

	if isSQLFile && !isTestFile {
		meta, err := metadata.ExtractAndValidate(string(content), unixPath)
		if err != nil {
			// Check if this is a "no metadata" error (not fatal)
			if !errors.Is(err, metadata.ErrNoMetadata) {
				// ExtractAndValidate was handed unixPath and names it itself.
				return pgmi.FileMetadata{}, err
			}
			// No metadata found - this is OK, file will use fallback identity
		} else {
			// Metadata found and valid - convert to public type
			scriptMetadata = &pgmi.ScriptMetadata{
				ID:          meta.ID,
				Idempotent:  *meta.Idempotent,
				SortKeys:    meta.SortKeys.Keys,
				Description: meta.Description,
			}
		}
	}

	return pgmi.FileMetadata{
		Path:        unixPath,
		Directory:   directory,
		Extension:   extension,
		Content:     string(content),
		Checksum:    checksumNormalized,
		ChecksumRaw: checksumRaw,
		Metadata:    scriptMetadata,
	}, nil
}

// ValidateDeploySQL checks if deploy.sql exists in the source directory.
func (s *Scanner) ValidateDeploySQL(sourcePath string) error {
	pathInfo, pathErr := s.fsProvider.Stat(sourcePath)
	if pathErr != nil {
		// "%s" not %q: %q escapes the separators in a Windows path, and a path
		// the user cannot paste back into a shell is not a remediation.
		return fmt.Errorf("project path \"%s\" does not exist: %w", sourcePath, pgmi.ErrInvalidConfig)
	}
	if !pathInfo.IsDir() {
		return fmt.Errorf("\"%s\" is a file; pass the project directory: %w", sourcePath, pgmi.ErrInvalidConfig)
	}

	deploySQLPath := filepath.Join(sourcePath, "deploy.sql")
	info, err := s.fsProvider.Stat(deploySQLPath)
	if err != nil {
		return fmt.Errorf("%w in %s\nexpected: %s — run `pgmi init` to scaffold one", pgmi.ErrDeploySQLNotFound, sourcePath, deploySQLPath)
	}

	if info.IsDir() {
		return fmt.Errorf("%s is a directory, expected a regular file: %w", deploySQLPath, pgmi.ErrInvalidConfig)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (a named pipe, socket or device): %w", deploySQLPath, pgmi.ErrInvalidConfig)
	}

	return nil
}

// ReadDeploySQL reads the deploy.sql file content.
func (s *Scanner) ReadDeploySQL(sourcePath string) (string, error) {
	deploySQLPath := filepath.Join(sourcePath, "deploy.sql")

	content, err := s.fsProvider.ReadFile(deploySQLPath)
	if err != nil {
		return "", fmt.Errorf("failed to read deploy.sql: %w", err)
	}

	content = bytes.TrimPrefix(content, utf8BOM)

	if i := bytes.IndexByte(content, 0); i >= 0 {
		return "", fmt.Errorf("deploy.sql contains a NUL byte at offset %d; PostgreSQL cannot take it in a query: %w", i, pgmi.ErrInvalidConfig)
	}
	if !utf8.Valid(content) {
		offset := findInvalidUTF8(content)
		return "", fmt.Errorf("deploy.sql is not valid UTF-8 (first invalid byte at offset %d); re-save the file as UTF-8 without BOM: %w", offset, pgmi.ErrInvalidConfig)
	}

	return string(content), nil
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func findInvalidUTF8(b []byte) int {
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			return i
		}
		i += size
	}
	return len(b)
}

// Verify Scanner implements the interface at compile time
var _ pgmi.FileScanner = (*Scanner)(nil)
