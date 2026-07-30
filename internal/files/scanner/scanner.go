package scanner

import (
	"bytes"
	"errors"
	"fmt"
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

	var files []pgmi.FileMetadata

	// Walk the directory tree
	err = dir.Walk(func(file filesystem.File, err error) error {
		if err != nil {
			return fmt.Errorf("error walking path: %w", err)
		}

		// Skip directories
		if file.Info().IsDir() {
			return nil
		}

		relPath := file.RelativePath()

		if isExcludedPath(relPath) {
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
		Files: files,
	}, nil
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

	// Calculate depth (number of directory segments after ./)
	// e.g., "./" = 0, "./migrations/" = 1, "./__test__/auth/" = 2
	depth := max(strings.Count(directory, "/")-1, 0)

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
		return pgmi.FileMetadata{}, fmt.Errorf("file %s contains a NUL byte; pgmi loads project files as text, so move this file outside the project path or into a hidden directory", unixPath)
	}
	if !utf8.Valid(content) {
		return pgmi.FileMetadata{}, fmt.Errorf("file %s is not valid UTF-8; pgmi loads project files as text", unixPath)
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
		Name:        filename,
		Directory:   directory,
		Extension:   extension,
		Depth:       depth,
		Content:     string(content),
		SizeBytes:   info.Size(),
		Checksum:    checksumNormalized,
		ChecksumRaw: checksumRaw,
		ModifiedAt:  info.ModTime(),
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
		return fmt.Errorf("%s is a directory, expected a regular file", deploySQLPath)
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

	if !utf8.Valid(content) {
		offset := findInvalidUTF8(content)
		return "", fmt.Errorf("deploy.sql is not valid UTF-8 (first invalid byte at offset %d); re-save the file as UTF-8 without BOM", offset)
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
