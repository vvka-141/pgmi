package pgmi

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Exit codes for semantic error classification.
// These follow Unix/GNU conventions:
//   - 0: Success
//   - 1: General error
//   - 2: CLI usage error (misuse of command line)
//   - 3+: Application-specific errors
const (
	ExitSuccess          = 0  // Deployment/test completed successfully
	ExitGeneralError     = 1  // Unknown or unclassified error
	ExitUsageError       = 2  // CLI usage error (missing args, invalid flags)
	ExitPanic            = 3  // Internal panic (unexpected crash)
	ExitConfigError      = 10 // Invalid configuration or parameters
	ExitConnectionError  = 11 // Failed to connect to database
	ExitApprovalDenied   = 12 // User denied overwrite approval
	ExitExecutionFailed  = 13 // SQL execution failed
	ExitDeploySQLMissing = 14 // deploy.sql not found
)

const (
	// DefaultForceApprovalCountdown is the countdown duration before force approval proceeds.
	DefaultForceApprovalCountdown = 5 * time.Second

	// DefaultRetryInitialDelay is the default initial delay before the first retry attempt.
	DefaultRetryInitialDelay = 100 * time.Millisecond

	// DefaultRetryMaxDelay is the default maximum delay between retry attempts.
	DefaultRetryMaxDelay = 1 * time.Minute

	// DefaultRetryMaxAttempts is the default maximum number of retry attempts.
	DefaultRetryMaxAttempts = 3

	// MaxErrorPreviewLength is the maximum number of characters shown
	// in error messages when previewing failed SQL batches.
	// This prevents overwhelming the console with large SQL statement errors.
	MaxErrorPreviewLength = 200

	// DefaultManagementDB is the default database to connect to for management operations.
	DefaultManagementDB = "postgres"
)

var (
	// DunderDirRegexp matches dunder directories (e.g., /__test__/, /__tests__/) in paths.
	DunderDirRegexp = regexp.MustCompile(`/__([^/]+)__/`)

	// AllowedDunderDirs lists the dunder directory names that pgmi recognizes.
	AllowedDunderDirs = map[string]bool{
		"test":  true,
		"tests": true,
	}
)

// IsTestPath returns true if the path contains a recognized test directory (/__test__/ or /__tests__/).
func IsTestPath(path string) bool {
	matches := DunderDirRegexp.FindAllStringSubmatch(path, -1)
	for _, m := range matches {
		if m[1] == "test" || m[1] == "tests" {
			return true
		}
	}
	return false
}

// ValidateDunderDirectories checks that all dunder directories in the path are recognized.
// Returns an error if an unsupported dunder directory is found.
func ValidateDunderDirectories(path string) error {
	matches := DunderDirRegexp.FindAllStringSubmatch(path, -1)
	for _, m := range matches {
		if !AllowedDunderDirs[m[1]] {
			return fmt.Errorf("unsupported directory \"__%s__\" in path %q; only __test__ and __tests__ are allowed", m[1], path)
		}
	}
	return nil
}

// SQLExtensions lists recognized SQL file extensions.
// Must stay consistent with pg_temp.pgmi_is_sql_file() in schema.sql.
var SQLExtensions = map[string]bool{
	".sql":     true,
	".ddl":     true,
	".dml":     true,
	".dql":     true,
	".dcl":     true,
	".psql":    true,
	".pgsql":   true,
	".plpgsql": true,
}

// IsSQLExtension returns true if the extension indicates a SQL file.
// Must stay consistent with pg_temp.pgmi_is_sql_file() in schema.sql.
func IsSQLExtension(ext string) bool {
	return SQLExtensions[strings.ToLower(ext)]
}
