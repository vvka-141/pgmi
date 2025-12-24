package pgmi

import "context"

// Approver handles user interaction for approval workflows,
// particularly for destructive operations like database overwriting.
//
// Implementations:
//   - ForcedApprover: Shows countdown and automatically approves
//   - InteractiveApprover: Prompts user to type database name for confirmation
type Approver interface {
	// RequestApproval prompts for confirmation before dropping and recreating a database.
	//
	// Parameters:
	//   - ctx: Context for cancellation
	//   - dbName: Name of the database to be overwritten
	//
	// Returns:
	//   - bool: true if approved, false if denied
	//   - error: Any error that occurred during the approval process
	RequestApproval(ctx context.Context, dbName string) (bool, error)
}
