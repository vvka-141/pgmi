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
	Files     []FileMetadata
	DeploySQL string
	// Ignored lists each path discovery left out, highest first: an ignored
	// directory appears once, not file by file.
	Ignored []IgnoredPath
}

// IgnoredPath is a project path that discovery did not load, and why.
type IgnoredPath struct {
	// Path in ./ form; a directory ends with /.
	Path string
	// Rule is IgnoreRuleBuiltIn or IgnoreRulePgmiignore.
	Rule string
}

const (
	IgnoreRuleBuiltIn    = "built-in"
	IgnoreRulePgmiignore = ".pgmiignore"
)

// PgmiignoreFile is the gitignore-style file at the project root whose
// patterns add to the built-in exclusions.
const PgmiignoreFile = ".pgmiignore"
