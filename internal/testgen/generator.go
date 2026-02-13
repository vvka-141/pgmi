package testgen

import (
	"fmt"
	"strings"

	"github.com/vvka-141/pgmi/internal/sourcemap"
)

// PlanRow represents a row from pg_temp.pgmi_test_plan() function.
type PlanRow struct {
	Ordinal    int
	StepType   string
	ScriptPath *string
	Directory  string
	Depth      int
}

// Step type constants
const (
	StepTypeFixture  = "fixture"
	StepTypeTest     = "test"
	StepTypeTeardown = "teardown"
)

// GeneratedSQL holds the generated SQL and its source map for error attribution.
type GeneratedSQL struct {
	SQL       string
	SourceMap *sourcemap.SourceMap
}

// PlanGenerator produces EXECUTE-based SQL that fetches content from pgmi_test_source.
type PlanGenerator struct{}

// NewPlanGenerator creates a new PlanGenerator.
func NewPlanGenerator() *PlanGenerator {
	return &PlanGenerator{}
}

// Generate converts execution plan rows to SQL for direct execution.
// Content is fetched at runtime from pgmi_test_source using EXECUTE.
// The caller must ensure this runs within a transaction (savepoints require it).
func (g *PlanGenerator) Generate(rows []PlanRow, callback string) *GeneratedSQL {
	result := &GeneratedSQL{
		SourceMap: sourcemap.New(),
	}

	if len(rows) == 0 {
		return result
	}

	var lines []string
	lineNum := 1

	// Track savepoints per directory
	dirSavepoints := make(map[string]string)
	testSavepoints := make(map[string]string)
	spCounter := 0

	// Suite start callback
	if callback != "" {
		lines = append(lines, FormatCallbackInvocation(callback, EventSuiteStart, nil, "", 0, 0))
		lineNum++
	}

	for _, row := range rows {
		switch row.StepType {
		case StepTypeFixture:
			// Create directory savepoint
			sp := fmt.Sprintf("__pgmi_d%d__", spCounter)
			spCounter++
			dirSavepoints[row.Directory] = sp

			lines = append(lines, fmt.Sprintf("SAVEPOINT %s;", sp))
			lineNum++

			// Fixture start callback
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventFixtureStart, row.ScriptPath, row.Directory, row.Depth, row.Ordinal))
				lineNum++
			}

			// Execute fixture content from pgmi_test_source (wrapped in DO block for PL/pgSQL context)
			if row.ScriptPath != nil {
				lines = append(lines, fmt.Sprintf("DO $__pgmi__$ BEGIN EXECUTE (SELECT content FROM pg_temp._pgmi_test_source WHERE path = %s); END $__pgmi__$;", quoteLiteral(*row.ScriptPath)))
				result.SourceMap.Add(lineNum, lineNum, *row.ScriptPath, 1, fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath))
				lineNum++
			}

			// Fixture end callback
			// NOTE: Test savepoint is created just before first test, not here.
			// This keeps test isolation separate from directory isolation.
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventFixtureEnd, row.ScriptPath, row.Directory, row.Depth, row.Ordinal))
				lineNum++
			}

		case StepTypeTest:
			// Ensure dir savepoint exists (handle no-fixture case)
			if _, ok := dirSavepoints[row.Directory]; !ok {
				sp := fmt.Sprintf("__pgmi_d%d__", spCounter)
				spCounter++
				dirSavepoints[row.Directory] = sp
				lines = append(lines, fmt.Sprintf("SAVEPOINT %s;", sp))
				lineNum++
			}

			// Create test savepoint just before first test for this directory
			// All tests in the same directory share this savepoint for rollback
			if _, ok := testSavepoints[row.Directory]; !ok {
				tsp := fmt.Sprintf("__pgmi_t%d__", spCounter)
				spCounter++
				testSavepoints[row.Directory] = tsp
				lines = append(lines, fmt.Sprintf("SAVEPOINT %s;", tsp))
				lineNum++
			}

			// Test start callback
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventTestStart, row.ScriptPath, row.Directory, row.Depth, row.Ordinal))
				lineNum++
			}

			// Execute test content from pgmi_test_source (wrapped in DO block for PL/pgSQL context)
			if row.ScriptPath != nil {
				lines = append(lines, fmt.Sprintf("DO $__pgmi__$ BEGIN EXECUTE (SELECT content FROM pg_temp._pgmi_test_source WHERE path = %s); END $__pgmi__$;", quoteLiteral(*row.ScriptPath)))
				result.SourceMap.Add(lineNum, lineNum, *row.ScriptPath, 1, fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath))
				lineNum++
			}

			// Test end callback (before rollback)
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventTestEnd, row.ScriptPath, row.Directory, row.Depth, row.Ordinal))
				lineNum++
			}

			// Rollback to test savepoint
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventRollback, row.ScriptPath, row.Directory, row.Depth, row.Ordinal))
				lineNum++
			}
			lines = append(lines, fmt.Sprintf("ROLLBACK TO SAVEPOINT %s;", testSavepoints[row.Directory]))
			lineNum++

		case StepTypeTeardown:
			// Teardown start callback
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventTeardownStart, nil, row.Directory, row.Depth, row.Ordinal))
				lineNum++
			}

			sp := dirSavepoints[row.Directory]
			if sp != "" {
				lines = append(lines, fmt.Sprintf("ROLLBACK TO SAVEPOINT %s;", sp))
				lineNum++
				lines = append(lines, fmt.Sprintf("RELEASE SAVEPOINT %s;", sp))
				lineNum++
			}

			// Teardown end callback
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventTeardownEnd, nil, row.Directory, row.Depth, row.Ordinal))
				lineNum++
			}
		}
	}

	// Suite end callback
	ordinal := 0
	if len(rows) > 0 {
		ordinal = rows[len(rows)-1].Ordinal
	}
	if callback != "" {
		lines = append(lines, FormatCallbackInvocation(callback, EventSuiteEnd, nil, "", 0, ordinal))
	}

	result.SQL = strings.Join(lines, "\n")
	return result
}

// quoteLiteral quotes a string for use as a PostgreSQL string literal.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
