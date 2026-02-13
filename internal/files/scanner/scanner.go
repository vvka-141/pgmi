package scanner

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/metadata"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Scanner discovers and processes files from a directory tree.
// All file types are loaded into pg_temp.pgmi_source; use is_sql_file column
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
		return pgmi.FileScanResult{}, fmt.Errorf("failed to open directory: %w", err)
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

		// Exclude deploy.sql (case-insensitive)
		if strings.ToLower(filepath.Base(file.Path())) == "deploy.sql" {
			return nil
		}

		// Process the file
		fileMetadata, err := s.processFile(file)
		if err != nil {
			return fmt.Errorf("failed to process file %s: %w", relPath, err)
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

// processFile reads a file and generates its metadata.
// For SQL files, it also extracts and validates PGMI metadata from <pgmi:meta> XML blocks.
func (s *Scanner) processFile(file filesystem.File) (pgmi.FileMetadata, error) {
	// Read file content
	content, err := file.ReadContent()
	if err != nil {
		return pgmi.FileMetadata{}, fmt.Errorf("failed to read file: %w", err)
	}

	info := file.Info()
	relativePath := file.RelativePath()

	// Convert path to Unix-style (forward slashes) and ensure ./ prefix
	unixPath := filepath.ToSlash(relativePath)
	if !strings.HasPrefix(unixPath, "./") {
		unixPath = "./" + unixPath
	}

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
	depth := strings.Count(directory, "/") - 1
	if depth < 0 {
		depth = 0
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

	var scriptMetadata *pgmi.ScriptMetadata
	isTestFile := pgmi.IsTestPath(unixPath)
	isSQLFile := isSQLExtension(extension)

	if isSQLFile && !isTestFile {
		meta, err := metadata.ExtractAndValidate(string(content), unixPath)
		if err != nil {
			// Check if this is a "no metadata" error (not fatal)
			if !errors.Is(err, metadata.ErrNoMetadata) {
				// Invalid metadata found - fail fast with precise error
				return pgmi.FileMetadata{}, fmt.Errorf("metadata error in %s: %w", unixPath, err)
			}
			// No metadata found - this is OK, file will use fallback identity
		} else {
			// Metadata found and valid - convert to public type
			scriptMetadata = &pgmi.ScriptMetadata{
				ID:          meta.ID,
				Idempotent:  meta.Idempotent,
				SortKeys:    meta.SortKeys.Keys,
				Description: meta.Description,
			}
		}
	}

	return pgmi.FileMetadata{
		Path:            unixPath,
		Name:            filename,
		Directory:       directory,
		Extension:       extension,
		Depth:           depth,
		Content:         string(content),
		SizeBytes:       info.Size(),
		Checksum:        checksumNormalized,
		ChecksumRaw:     checksumRaw,
		ModifiedAt:      info.ModTime(),
		Metadata:        scriptMetadata,
	}, nil
}

// isSQLExtension checks if the file extension indicates a SQL file.
// Matches the pattern used in pg_temp.pgmi_is_sql_file().
func isSQLExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".sql", ".ddl", ".dml", ".dql", ".dcl", ".psql", ".pgsql", ".plpgsql":
		return true
	default:
		return false
	}
}

// ValidateDeploySQL checks if deploy.sql exists in the source directory.
func (s *Scanner) ValidateDeploySQL(sourcePath string) error {
	deploySQLPath := filepath.Join(sourcePath, "deploy.sql")
	info, err := s.fsProvider.Stat(deploySQLPath)
	if err != nil {
		return fmt.Errorf("deploy.sql not found in source directory: %s (error: %w)", sourcePath, err)
	}

	if info.IsDir() {
		return fmt.Errorf("deploy.sql is a directory, not a file: %s", deploySQLPath)
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

	return string(content), nil
}

// Verify Scanner implements the interface at compile time
var _ pgmi.FileScanner = (*Scanner)(nil)
