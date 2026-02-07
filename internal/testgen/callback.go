package testgen

import "fmt"

const (
	EventSuiteStart    = "suite_start"
	EventSuiteEnd      = "suite_end"
	EventFixtureStart  = "fixture_start"
	EventFixtureEnd    = "fixture_end"
	EventTestStart     = "test_start"
	EventTestEnd       = "test_end"
	EventRollback      = "rollback"
	EventTeardownStart = "teardown_start"
	EventTeardownEnd   = "teardown_end"
)

// FormatCallbackInvocation generates a SQL SELECT statement that invokes the callback
// with a pgmi_test_event composite type.
func FormatCallbackInvocation(callback, event string, path *string, dir string, depth, ordinal int) string {
	pathSQL := "NULL"
	if path != nil {
		pathSQL = fmt.Sprintf("'%s'", EscapeSQLString(*path))
	}
	return fmt.Sprintf(
		"SELECT %s(ROW('%s', %s, '%s', %d, %d, NULL)::pg_temp.pgmi_test_event);",
		callback, event, pathSQL, EscapeSQLString(dir), depth, ordinal,
	)
}
