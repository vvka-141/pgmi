package testgen

import (
	"fmt"
	"strings"

	"github.com/vvka-141/pgmi/internal/sourcemap"
	"github.com/vvka-141/pgmi/internal/testdiscovery"
)

// PlanModeGenerator produces a single PERFORM pgmi_plan_command() call
// containing the entire test execution sequence with embedded content.
type PlanModeGenerator struct{}

// NewPlanModeGenerator creates a new PlanModeGenerator.
func NewPlanModeGenerator() *PlanModeGenerator {
	return &PlanModeGenerator{}
}

// Generate converts execution plan rows to pgmi_plan_command() calls.
// Outputs embedded SQL content directly instead of calling helper functions.
// The generated SQL schedules BEGIN, test execution, and COMMIT as separate commands.
func (g *PlanModeGenerator) Generate(rows []testdiscovery.TestScriptRow) *GeneratedSQL {
	return g.GenerateWithCallback(rows, "")
}

// GenerateWithCallback converts execution plan rows to pgmi_plan_command() calls with optional callback invocations.
// When callback is empty, behaves exactly as Generate().
// When callback is specified, emits callback invocations inside the dollar-quoted block.
func (g *PlanModeGenerator) GenerateWithCallback(rows []testdiscovery.TestScriptRow, callback string) *GeneratedSQL {
	result := &GeneratedSQL{
		SourceMap: sourcemap.New(),
	}

	if len(rows) == 0 {
		return result
	}

	var allLines []string
	// Line 1 is BEGIN command
	lineNum := 1

	// Schedule BEGIN for savepoint support
	allLines = append(allLines, "PERFORM pg_temp.pgmi_plan_command('BEGIN;');")
	lineNum++

	var innerLines []string
	// Inner SQL line tracking starts after BEGIN command
	innerStartLine := lineNum + 1 // +1 for "PERFORM pg_temp.pgmi_plan_command($__pgmi__$"

	ordinal := 0

	// Suite start callback (inside dollar-quoted block)
	if callback != "" {
		innerLines = append(innerLines, FormatCallbackExistenceCheck(callback))
		innerLines = append(innerLines, FormatCallbackInvocation(callback, EventSuiteStart, nil, "", 0, 0))
	}

	for _, row := range rows {
		ordinal++
		startLine := innerStartLine + len(innerLines)

		switch row.StepType {
		case "fixture":
			// Fixture start callback
			if callback != "" {
				innerLines = append(innerLines, FormatCallbackInvocation(callback, EventFixtureStart, row.ScriptPath, row.Directory, row.Depth, ordinal))
			}

			if row.PreExec != nil {
				innerLines = append(innerLines, *row.PreExec)
			}

			if row.ScriptSQL != nil {
				content := *row.ScriptSQL
				innerLines = append(innerLines, content)
				contentLines := strings.Count(content, "\n") + 1
				if row.ScriptPath != nil {
					desc := fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath)
					result.SourceMap.Add(startLine+len(innerLines)-1, startLine+len(innerLines)-1+contentLines-1, *row.ScriptPath, 1, desc)
				}
			}

			if row.PostExec != nil {
				innerLines = append(innerLines, *row.PostExec)
			}

			// Fixture end callback
			if callback != "" {
				innerLines = append(innerLines, FormatCallbackInvocation(callback, EventFixtureEnd, row.ScriptPath, row.Directory, row.Depth, ordinal))
			}

		case "test":
			// Test start callback
			if callback != "" {
				innerLines = append(innerLines, FormatCallbackInvocation(callback, EventTestStart, row.ScriptPath, row.Directory, row.Depth, ordinal))
			}

			if row.PreExec != nil {
				innerLines = append(innerLines, *row.PreExec)
			}

			if row.ScriptSQL != nil {
				content := *row.ScriptSQL
				innerLines = append(innerLines, content)
				contentLines := strings.Count(content, "\n") + 1
				if row.ScriptPath != nil {
					desc := fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath)
					result.SourceMap.Add(startLine+len(innerLines)-1, startLine+len(innerLines)-1+contentLines-1, *row.ScriptPath, 1, desc)
				}
			}

			// Test end callback
			if callback != "" {
				innerLines = append(innerLines, FormatCallbackInvocation(callback, EventTestEnd, row.ScriptPath, row.Directory, row.Depth, ordinal))
			}

			if row.PostExec != nil {
				// Rollback callback
				if callback != "" {
					innerLines = append(innerLines, FormatCallbackInvocation(callback, EventRollback, row.ScriptPath, row.Directory, row.Depth, ordinal))
				}
				innerLines = append(innerLines, *row.PostExec)
			}

		case "teardown":
			// Teardown start callback
			if callback != "" {
				innerLines = append(innerLines, FormatCallbackInvocation(callback, EventTeardownStart, nil, row.Directory, row.Depth, ordinal))
			}

			if row.PreExec != nil {
				innerLines = append(innerLines, *row.PreExec)
			}

			if row.PostExec != nil {
				innerLines = append(innerLines, *row.PostExec)
			}

			// Source map for teardown
			desc := fmt.Sprintf("%s: %s", row.StepType, row.Directory)
			endLine := startLine + 1
			if row.PreExec != nil {
				endLine++
			}
			if row.PostExec != nil {
				endLine++
			}
			result.SourceMap.Add(startLine, endLine-1, row.Directory, 0, desc)

			// Teardown end callback
			if callback != "" {
				innerLines = append(innerLines, FormatCallbackInvocation(callback, EventTeardownEnd, nil, row.Directory, row.Depth, ordinal))
			}

		default:
			if row.PreExec != nil {
				innerLines = append(innerLines, *row.PreExec)
			}

			if row.ScriptSQL != nil {
				content := *row.ScriptSQL
				innerLines = append(innerLines, content)
				contentLines := strings.Count(content, "\n") + 1
				if row.ScriptPath != nil {
					desc := fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath)
					result.SourceMap.Add(startLine+len(innerLines)-1, startLine+len(innerLines)-1+contentLines-1, *row.ScriptPath, 1, desc)
				}
			}

			if row.PostExec != nil {
				innerLines = append(innerLines, *row.PostExec)
			}
		}
	}

	// Suite end callback
	if callback != "" {
		innerLines = append(innerLines, FormatCallbackInvocation(callback, EventSuiteEnd, nil, "", 0, ordinal))
	}

	// Schedule test execution as single command
	innerSQL := strings.Join(innerLines, "\n")
	allLines = append(allLines, fmt.Sprintf("PERFORM pg_temp.pgmi_plan_command($__pgmi__$\n%s\n$__pgmi__$);", innerSQL))

	// Schedule COMMIT
	allLines = append(allLines, "PERFORM pg_temp.pgmi_plan_command('COMMIT;');")

	result.SQL = strings.Join(allLines, "\n")
	return result
}
