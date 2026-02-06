package services

// SQL query constants for deployment operations.
// Centralizing queries here improves maintainability and follows the project
// philosophy of keeping SQL separate from Go code.

const (
	// queryPlanCommands retrieves all planned commands from the execution plan
	// ordered by their execution sequence.
	queryPlanCommands = `
		SELECT ordinal, command_sql
		FROM pg_temp.pgmi_plan
		ORDER BY ordinal
	`

	// queryTestPlan retrieves the filtered test execution plan including
	// fixture, test, and teardown scripts with embedded content.
	// Parameter $1: POSIX regex filter pattern
	queryTestPlan = `
		SELECT ordinal, step_type, script_path, pre_exec, script_sql, post_exec
		FROM pg_temp.pgmi_unittest_pvw_plan($1)
		ORDER BY ordinal
	`

	// queryTestPlanList retrieves the filtered test execution plan metadata
	// for list-only mode (dry-run without execution).
	// Parameter $1: POSIX regex filter pattern
	queryTestPlanList = `
		SELECT ordinal, step_type, script_path
		FROM pg_temp.pgmi_unittest_pvw_plan($1)
		ORDER BY ordinal
	`

	// queryTestScriptRows retrieves all test script rows for macro expansion.
	// Used by the preprocessor to expand pgmi_test() and pgmi_plan_test() macros.
	queryTestScriptRows = `
		SELECT ordinal, step_type, script_path, directory, depth, pre_exec, script_sql, post_exec
		FROM pg_temp.pgmi_test_plan
		ORDER BY ordinal
	`
)
