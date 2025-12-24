package pgmi

import "time"

// Exit codes for semantic error classification.
// These follow Unix conventions: 0=success, 1=general error, 2=panic.
// Higher codes (10+) provide fine-grained error categorization.
const (
	ExitSuccess          = 0  // Deployment/test completed successfully
	ExitGeneralError     = 1  // Unknown or unclassified error
	ExitPanic            = 2  // Internal panic (unexpected crash)
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

	// TestDirectoryPattern is the directory pattern used to identify test files.
	// Test files must be located in directories matching this pattern (e.g., ./migrations/__test__/test_foo.sql).
	// This pattern is used for:
	//   1. File scanning: Automatically moving test files to pg_temp.pgmi_unittest_script
	//   2. Test discovery: Filtering files for unit test execution
	//   3. SQL queries: Identifying test files in unittest.sql
	TestDirectoryPattern = "/__test__/"
)
