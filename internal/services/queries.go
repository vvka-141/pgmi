package services

// SQL query constants for deployment operations.
// Centralizing queries here improves maintainability and follows the project
// philosophy of keeping SQL separate from Go code.

const (
	// queryTestScriptRows retrieves all test script rows for macro expansion.
	// Used by the preprocessor to expand pgmi_test() macros.
	// Note: This is only used as a fallback when files are not available.
	queryTestScriptRows = `
		SELECT ordinal, step_type, script_path, directory, depth, pre_exec, script_sql, post_exec
		FROM pg_temp.pgmi_test_plan
		ORDER BY ordinal
	`
)
