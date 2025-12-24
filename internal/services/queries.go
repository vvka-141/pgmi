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
	// setup, test, and teardown scripts.
	// Parameter $1: POSIX regex filter pattern
	queryTestPlan = `
		SELECT execution_order, step_type, script_path, executable_sql
		FROM pg_temp.pgmi_unittest_pvw_plan($1)
		ORDER BY execution_order
	`

	// queryTestPlanList retrieves the filtered test execution plan metadata
	// for list-only mode (dry-run without execution).
	// Parameter $1: POSIX regex filter pattern
	queryTestPlanList = `
		SELECT execution_order, step_type, script_path
		FROM pg_temp.pgmi_unittest_pvw_plan($1)
		ORDER BY execution_order
	`
)
