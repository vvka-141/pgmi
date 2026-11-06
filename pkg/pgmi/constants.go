package pgmi

// Exit codes for semantic error classification.
// These follow Unix/GNU conventions:
//   - 0: Success
//   - 1: General error
//   - 2: CLI usage error (misuse of command line)
//   - 3+: Application-specific errors
const (
	ExitSuccess          = 0   // Deployment/test completed successfully
	ExitGeneralError     = 1   // Unknown or unclassified error
	ExitUsageError       = 2   // CLI usage error (missing args, invalid flags)
	ExitPanic            = 3   // Internal panic (unexpected crash)
	ExitConfigError      = 10  // Invalid pgmi configuration, rejected before anything is created, dropped or deployed
	ExitConnectionError  = 11  // Failed to connect to database
	ExitApprovalDenied   = 12  // User denied overwrite approval
	ExitExecutionFailed  = 13  // SQL execution failed
	ExitDeploySQLMissing = 14  // deploy.sql not found
	ExitConcurrentDeploy = 15  // Another pgmi deployment is in progress against the same database
	ExitTimeout          = 16  // Operation exceeded --timeout (context deadline exceeded)
	ExitInterrupted      = 130 // Process interrupted by SIGINT (Ctrl-C) or SIGTERM — Unix convention 128+SIGINT
)
