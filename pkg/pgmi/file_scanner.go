package pgmi

// FileScanner defines the interface for discovering and processing SQL files.
// Implementations must be safe for concurrent use by multiple goroutines.
type FileScanner interface {
	// ScanDirectory recursively scans a directory and returns file metadata.
	// Excludes deploy.sql from the results as it's the orchestrator script.
	ScanDirectory(sourcePath string) (FileScanResult, error)

	// ValidateDeploySQL checks if deploy.sql exists in the source directory.
	ValidateDeploySQL(sourcePath string) error

	// ReadDeploySQL reads the deploy.sql file content.
	ReadDeploySQL(sourcePath string) (string, error)
}

// FileScanResult contains the results of scanning a directory.
type FileScanResult struct {
	Files []FileMetadata
}
